#!/bin/sh

set -e

usage() {
	echo "Usage: $0 create <hostpath> <vg> <name> <pvid> <size>"
	echo "       $0 delete <hostpath> <pvid>"
	exit 1
}

case "$1" in
	create)
		hostpath="$2"
		vgname="$3"
		pvname="$4"
		pvid="$5"
		pvsize="$6"

		[ -n "$hostpath" -a -n "$vgname" -a -n "$pvname" -a -n "$pvid" -a -n "$pvsize" ] || usage
		lvcreate -y -L "${pvsize}B" -n "$pvid" --addtag local-lvm-provisioner --addtag "pvname=$pvname" "$vgname"
		mkfs.ext4 -m 0 -L "$pvname" -U "${pvid#pvc-}" "/dev/$vgname/$pvid"
		nsenter -t 1 -m -- mkdir -p "$hostpath/$pvname"
		nsenter -t 1 -m -- mount "/dev/$vgname/$pvid" "$hostpath/$pvname"
		nsenter -t 1 -m -- chmod 0777 "$hostpath/$pvname"
		nsenter -t 1 -m -- pvscan --cache
	;;
	delete)
		hostpath="$2"
		pvid="$3"
		pvname=""

		[ -n "$hostpath" -a -n "$pvid" ] || usage

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
				pvname=*)
					pvname="${tag#pvname=}"
					break
				;;
			esac
		done

		if [ -z "$pvname" ]; then
			echo "Unable to determine volume name from PVID '$pvid': no name tag associated" >&2
			exit 1
		fi

		nsenter -t 1 -m -- umount "$hostpath/$pvname" || true
		nsenter -t 1 -m -- rmdir "$hostpath/$pvname" || true
		lvchange -a n "$vgname/$pvid"
		lvremove "$vgname/$pvid"
		nsenter -t 1 -m -- pvscan --cache
	;;
	*)
		usage
	;;
esac
