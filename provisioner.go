package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"strconv"
	"sync"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/pkg/errors"

	pvController "github.com/kubernetes-incubator/external-storage/lib/controller"
	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
)

type ActionType string

const (
	ActionTypeCreate = "create"
	ActionTypeDelete = "delete"
)

const (
	KeyNode = "kubernetes.io/hostname"

	NodeDefaultNonListedNodes = "DEFAULT_VGS_FOR_NON_LISTED_NODES"
)

var (
	CmdTimeoutCounts = 120

	ConfigFileCheckInterval = 5 * time.Second
)

type LocalLVMProvisioner struct {
	stopCh      chan struct{}
	kubeClient  *clientset.Clientset
	namespace   string
	helperImage string

	config      *Config
	configData  *ConfigData
	configFile  string
	configMutex *sync.RWMutex
}

type NodeVGMapData struct {
	Node string `json:"node,omitempty"`
	Path string `json:"path,omitempty"`
	VGs[]string `json:"vgs,omitempty"`
}

type ConfigData struct {
	NodeVGMap []*NodeVGMapData `json:"NodeVGMap,omitempty"`
}

type NodeVGMap struct {
	Path string
	VGs map[string]struct{}
}

type Config struct {
	NodeVGMap map[string]*NodeVGMap
}

func NewProvisioner(stopCh chan struct{}, kubeClient *clientset.Clientset, configFile, namespace, helperImage string) (*LocalLVMProvisioner, error) {
	p := &LocalLVMProvisioner{
		stopCh: stopCh,

		kubeClient:  kubeClient,
		namespace:   namespace,
		helperImage: helperImage,

		// config will be updated shortly by p.refreshConfig()
		config:      nil,
		configFile:  configFile,
		configData:  nil,
		configMutex: &sync.RWMutex{},
	}
	if err := p.refreshConfig(); err != nil {
		return nil, err
	}
	p.watchAndRefreshConfig()
	return p, nil
}

func (p *LocalLVMProvisioner) refreshConfig() error {
	p.configMutex.Lock()
	defer p.configMutex.Unlock()

	configData, err := loadConfigFile(p.configFile)
	if err != nil {
		return err
	}
	// no need to update
	if reflect.DeepEqual(configData, p.configData) {
		return nil
	}
	config, err := canonicalizeConfig(configData)
	if err != nil {
		return err
	}
	// only update the config if the new config file is valid
	p.configData = configData
	p.config = config

	output, err := json.Marshal(p.configData)
	if err != nil {
		return err
	}
	logrus.Debugf("Applied config: %v", string(output))

	return err
}

func (p *LocalLVMProvisioner) watchAndRefreshConfig() {
	go func() {
		for {
			select {
			case <-time.Tick(ConfigFileCheckInterval):
				if err := p.refreshConfig(); err != nil {
					logrus.Errorf("failed to load the new config file: %v", err)
				}
			case <-p.stopCh:
				logrus.Infof("stop watching config file")
				return
			}
		}
	}()
}

func (p *LocalLVMProvisioner) getPathAndVGOnNode(node string) (string, string, error) {
	p.configMutex.RLock()
	defer p.configMutex.RUnlock()

	if p.config == nil {
		return "", "", fmt.Errorf("no valid config available")
	}

	c := p.config
	npMap := c.NodeVGMap[node]
	if npMap == nil {
		npMap = c.NodeVGMap[NodeDefaultNonListedNodes]
		if npMap == nil {
			return "", "", fmt.Errorf("config doesn't contain node %v, and no %v available", node, NodeDefaultNonListedNodes)
		}
		logrus.Debugf("config doesn't contain node %v, use %v instead", node, NodeDefaultNonListedNodes)
	}
	if npMap.Path == "" {
		return "", "", fmt.Errorf("no mount path defined on node %v", node)
	}
	vgs := npMap.VGs
	if len(vgs) == 0 {
		return "", "", fmt.Errorf("no local volume group available on node %v", node)
	}
	vg := ""
	for vg = range vgs {
		break
	}
	return npMap.Path, vg, nil
}

func (p *LocalLVMProvisioner) Provision(opts pvController.VolumeOptions) (*v1.PersistentVolume, error) {
	pvc := opts.PVC
	if pvc.Spec.Selector != nil {
		return nil, fmt.Errorf("claim.Spec.Selector is not supported")
	}
	for _, accessMode := range pvc.Spec.AccessModes {
		if accessMode != v1.ReadWriteOnce {
			return nil, fmt.Errorf("Only support ReadWriteOnce access mode")
		}
	}
	node := opts.SelectedNode
	if opts.SelectedNode == nil {
		return nil, fmt.Errorf("configuration error, no node was specified")
	}

	size, ok := pvc.Spec.Resources.Requests[v1.ResourceStorage]
	if !ok {
		return nil, fmt.Errorf("Cannot handle physical volume claim without storage size request")
	}

	if size.Value() < 1024 * 1024 * 4 {
		return nil, fmt.Errorf("Physical volume needs to be at least 4MB in size")
	}

	mountPath, vgName, err := p.getPathAndVGOnNode(node.Name)
	if err != nil {
		return nil, err
	}

	pvcNameParts := []string{ pvc.Namespace, pvc.Name }
	pvcName := strings.Join(pvcNameParts, "-")

	name := opts.PVName
	path := filepath.Join(mountPath, pvcName)
	logrus.Infof("Creating volume %v (%v/%v) at %v:%v", name, pvc.Namespace, pvc.Name, node.Name, path)

	createVGOperationArgs := []string{
		"create",
		mountPath,
		vgName,
		pvcName,
		name,
		strconv.FormatInt(size.Value(), 10),
	}
	if err := p.createHelperPod(ActionTypeCreate, createVGOperationArgs, node.Name); err != nil {
		return nil, err
	}

	fs := v1.PersistentVolumeFilesystem
	hostPathType := v1.HostPathDirectoryOrCreate
	return &v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: v1.PersistentVolumeSpec{
			PersistentVolumeReclaimPolicy: opts.PersistentVolumeReclaimPolicy,
			AccessModes:                   pvc.Spec.AccessModes,
			VolumeMode:                    &fs,
			Capacity: v1.ResourceList{
				v1.ResourceName(v1.ResourceStorage): pvc.Spec.Resources.Requests[v1.ResourceName(v1.ResourceStorage)],
			},
			PersistentVolumeSource: v1.PersistentVolumeSource{
				HostPath: &v1.HostPathVolumeSource{
					Path: path,
					Type: &hostPathType,
				},
			},
			NodeAffinity: &v1.VolumeNodeAffinity{
				Required: &v1.NodeSelector{
					NodeSelectorTerms: []v1.NodeSelectorTerm{
						{
							MatchExpressions: []v1.NodeSelectorRequirement{
								{
									Key:      KeyNode,
									Operator: v1.NodeSelectorOpIn,
									Values: []string{
										node.Name,
									},
								},
							},
						},
					},
				},
			},
		},
	}, nil
}

func (p *LocalLVMProvisioner) Delete(pv *v1.PersistentVolume) (err error) {
	defer func() {
		err = errors.Wrapf(err, "failed to delete volume %v", pv.Name)
	}()
	path, node, err := p.getPathAndNodeForPV(pv)
	if err != nil {
		return err
	}
	if pv.Spec.PersistentVolumeReclaimPolicy != v1.PersistentVolumeReclaimRetain {
		logrus.Infof("Deleting volume %v at %v:%v", pv.Name, node, path)
		cleanupVGOperationArgs := []string{"delete", path, pv.Name}
		if err := p.createHelperPod(ActionTypeDelete, cleanupVGOperationArgs, node); err != nil {
			logrus.Infof("clean up volume %v failed: %v", pv.Name, err)
			return err
		}
		return nil
	}
	logrus.Infof("Retained volume %v", pv.Name)
	return nil
}

func (p *LocalLVMProvisioner) getPathAndNodeForPV(pv *v1.PersistentVolume) (path, node string, err error) {
	defer func() {
		err = errors.Wrapf(err, "failed to delete volume %v", pv.Name)
	}()

	hostPath := pv.Spec.PersistentVolumeSource.HostPath
	if hostPath == nil {
		return "", "", fmt.Errorf("no HostPath set")
	}
	path = filepath.Dir(hostPath.Path)
	if path == "." || path == "/" {
		return "", "", fmt.Errorf("invalid HostPath set")
	}
	nodeAffinity := pv.Spec.NodeAffinity
	if nodeAffinity == nil {
		return "", "", fmt.Errorf("no NodeAffinity set")
	}
	required := nodeAffinity.Required
	if required == nil {
		return "", "", fmt.Errorf("no NodeAffinity.Required set")
	}

	node = ""
	for _, selectorTerm := range required.NodeSelectorTerms {
		for _, expression := range selectorTerm.MatchExpressions {
			if expression.Key == KeyNode && expression.Operator == v1.NodeSelectorOpIn {
				if len(expression.Values) != 1 {
					return "", "", fmt.Errorf("multiple values for the node affinity")
				}
				node = expression.Values[0]
				break
			}
		}
		if node != "" {
			break
		}
	}
	if node == "" {
		return "", "", fmt.Errorf("cannot find affinited node")
	}
	return path, node, nil
}

func (p *LocalLVMProvisioner) createHelperPod(action ActionType, vgOperationArgs []string, node string) (err error) {
	var name string

	if action == ActionTypeCreate {
		name = vgOperationArgs[4]
	} else {
		name = vgOperationArgs[2]
	}

	defer func() {
		err = errors.Wrapf(err, "failed to %v volume %v", action, name)
	}()

	if name == "" || node == "" {
		return fmt.Errorf("invalid empty name or node")
	}
	path, err := filepath.Abs(vgOperationArgs[1])
	if err != nil {
		return err
	}
	path = strings.TrimSuffix(path, "/")
	if path == "" {
		// it covers the `/` case
		return fmt.Errorf("invalid path for %v", action)
	}

	hostPathType := v1.HostPathDirectoryOrCreate
	privilegedTrue := true
	helperPod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: string(action) + "-" + name,
		},
		Spec: v1.PodSpec{
			RestartPolicy: v1.RestartPolicyNever,
			NodeName: node,
			HostPID: true,
			Tolerations: []v1.Toleration{
				{
					Operator: v1.TolerationOpExists,
				},
			},
			Containers: []v1.Container{
				{
					Name:  "local-lvm-" + string(action),
					Image: p.helperImage,
					Args:  vgOperationArgs,
					VolumeMounts: []v1.VolumeMount{
						{
							Name:      "data",
							ReadOnly:  false,
							MountPath: vgOperationArgs[1],
						},
					},
					SecurityContext: &v1.SecurityContext{
						Privileged: &privilegedTrue,
					},
				},
			},
			Volumes: []v1.Volume{
				{
					Name: "data",
					VolumeSource: v1.VolumeSource{
						HostPath: &v1.HostPathVolumeSource{
							Path: vgOperationArgs[1],
							Type: &hostPathType,
						},
					},
				},
			},
		},
	}

	pod, err := p.kubeClient.CoreV1().Pods(p.namespace).Create(helperPod)
	if err != nil {
		return err
	}

	defer func() {
		e := p.kubeClient.CoreV1().Pods(p.namespace).Delete(pod.Name, &metav1.DeleteOptions{})
		if e != nil {
			logrus.Errorf("unable to delete the helper pod: %v", e)
		}
	}()

	completed := false
	for i := 0; i < CmdTimeoutCounts; i++ {
		if pod, err := p.kubeClient.CoreV1().Pods(p.namespace).Get(pod.Name, metav1.GetOptions{}); err != nil {
			return err
		} else if pod.Status.Phase == v1.PodSucceeded {
			completed = true
			break
		}
		time.Sleep(1 * time.Second)
	}
	if !completed {
		return fmt.Errorf("create process timeout after %v seconds", CmdTimeoutCounts)
	}

	logrus.Infof("Volume %v has been %vd on %v:%v", name, action, node, path)
	return nil
}

func loadConfigFile(configFile string) (cfgData *ConfigData, err error) {
	defer func() {
		err = errors.Wrapf(err, "fail to load config file %v", configFile)
	}()
	f, err := os.Open(configFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var data ConfigData
	if err := json.NewDecoder(f).Decode(&data); err != nil {
		return nil, err
	}
	return &data, nil
}

func canonicalizeConfig(data *ConfigData) (cfg *Config, err error) {
	defer func() {
		err = errors.Wrapf(err, "config canonicalization failed")
	}()
	cfg = &Config{}
	cfg.NodeVGMap = map[string]*NodeVGMap{}
	for _, n := range data.NodeVGMap {
		if cfg.NodeVGMap[n.Node] != nil {
			return nil, fmt.Errorf("duplicate node %v", n.Node)
		}
		npMap := &NodeVGMap{VGs: map[string]struct{}{}}
		cfg.NodeVGMap[n.Node] = npMap

		if n.Path[0] != '/' {
			return nil, fmt.Errorf("mount path must start with / for path %v on node %v", n.Path, n.Node)
		}
		path, err := filepath.Abs(n.Path)
		if err != nil {
			return nil, err
		}
		if path == "/" {
			return nil, fmt.Errorf("cannot use root ('/') as mount path on node %v", n.Node)
		}
		npMap.Path = path

		for _, vg := range n.VGs {
			if _, ok := npMap.VGs[vg]; ok {
				return nil, fmt.Errorf("duplicate volume group %v on node %v", vg, n.Node)
			}
			npMap.VGs[vg] = struct{}{}
		}
	}
	return cfg, nil
}
