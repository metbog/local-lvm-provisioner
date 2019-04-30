#!/bin/sh

set -e

usage() {
	echo "Usage: $0 create <pvid> <hostpath> <vg> <name> <size>"
	echo "       $0 delete <pvid>"
	echo "       $0 run <arguments...>"
	exit 1
}

case "$1" in
	create)
		pvid="$2"
		hostpath="$3"
		vgname="$4"
		pvname="$5"
		pvsize="$6"

		[ -n "$hostpath" -a -n "$vgname" -a -n "$pvname" -a -n "$pvid" -a -n "$pvsize" ] || usage
		lvcreate -y -L "${pvsize}B" -n "$pvid" --addtag local-lvm-provisioner --addtag "mount=$hostpath" --addtag "pvname=$pvname" "$vgname"
		mkfs.ext4 -m 0 -L "$pvname" -U "${pvid#pvc-}" "/dev/$vgname/$pvid"
		nsenter -t 1 -m -- mkdir -p "$hostpath/$pvname"
		nsenter -t 1 -m -- mount "/dev/$vgname/$pvid" "$hostpath/$pvname"
		nsenter -t 1 -m -- chmod 0777 "$hostpath/$pvname"
		nsenter -t 1 -m -- pvscan --cache
	;;
	delete)
		pvid="$2"
		pvname=""
		hostpath=""

		[ -n "$pvid" ] || usage

		lvs="$(lvs --noheadings -o vg_name,lv_tags -S lv_name="$pvid" @local-lvm-provisioner)"

		if [ -z "$lvs" ]; then
			echo "Unable to determine volume name from PVID '$pvid': no matching volume found" >&2
			exit 1
		elif [ $(echo "$lvs" | wc -l) -gt 1 ]; then
			echo "Unable to determine volume name from PVID '$pvid': multiple matching volumes" >&2
			exit 1
		fi

		oIFS="$IFS"; IFS=" "; set -- $lvs; IFS="$oIFS"
		vgname="$1"
		lvtags="$2"

		oIFS="$IFS"; IFS=","; set -- $lvtags; IFS="$oIFS"
		for tag in "$@"; do
			case "$tag" in
				mount=*)
					hostpath="${tag#mount=}"
				;;
				pvname=*)
					pvname="${tag#pvname=}"
				;;
			esac
		done

		if [ -z "$pvname" ]; then
			echo "Unable to determine volume name from PVID '$pvid': no name tag associated" >&2
			exit 1
		elif [ -z "$hostpath" ]; then
			echo "Unable to determine volume mount path from PVID '$pvid': no mount tag associated" >&2
			exit 1
		fi

		nsenter -t 1 -m -- umount "$hostpath/$pvname" || true
		nsenter -t 1 -m -- rmdir "$hostpath/$pvname" || true
		lvchange -a n "$vgname/$pvid"
		lvremove "$vgname/$pvid"
		nsenter -t 1 -m -- pvscan --cache
	;;
	run)
		shift
		exec /bin/local-lvm-provisioner "$@"
	;;
	*)
		usage
	;;
esac
