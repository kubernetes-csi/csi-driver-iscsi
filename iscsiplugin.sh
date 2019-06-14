#! /bin/bash -x

# Update initiatorname with ${iSCSI_INITIATOR_NAME} and start iscsid, if it is defined
if [ -n "${iSCSI_INITIATOR_NAME}" ]; then
	echo "InitiatorName=${iSCSI_INITIATOR_NAME}" > /etc/iscsi/initiatorname.iscsi 
	# Start iscsid
	iscsid -f &
fi

# Start iscsiplugin
./iscsiplugin $*
