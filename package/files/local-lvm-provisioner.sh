#!/usr/bin/env bash

case "$1" in
	start)
		lvs --noheadings -o lv_path,lv_name,lv_tags @local-lvm-provisioner | while read line; do
			set -- $line
			lvpath="$1"
			lvname="$2"
			lvtags="$3"

			oIFS="$IFS"; IFS=","
			set -- $lvtags
			IFS="$oIFS"

			mountpath=""
			pvname=""

			for tag in $@; do
				case "$tag" in
					mount=*)
						mountpath="${tag#mount=}"
					;;
					pvname=*)
						pvname="${tag#pvname=}"
					;;
				esac
			done

			if [ -z "$mountpath" -o -z "$pvname" ]; then
				echo "Logical volume $lvname lacks mount path or name tag - skipping." >&2
				continue
			fi

			for try in 1 2 3 4 5; do
				[ -d "$mountpath/$pvname" ] && break
				echo "Waiting for mount point $mountpath/$pvname to appear ... $try/5" >&2
				sleep 1
			done

			if [ ! -d "$mountpath/$pvname" ]; then
				echo "Mount point $mountpath/$pvname for volume $lvname not found - skipping." >&2
				continue
			fi

			if mountpoint -q "$mountpath/$pvname"; then
				echo "Mount point $mountpath/$pvname for volume $lvname already mounted - skipping." >&2
				continue
			fi

			mount "$lvpath" "$mountpath/$pvname"
		done
	;;
	*)
		echo "Usage: $0 start" >&2
		exit 1
	;;
esac
