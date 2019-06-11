FROM centos:7.4.1708

# Copy iscsiplugin.sh
COPY iscsiplugin.sh /iscsiplugin.sh
# Copy iscsiplugin from build _output directory
COPY bin/iscsiplugin /iscsiplugin
RUN mkdir -p /targetd/
COPY targetd/bin/plugin.so /targetd/plugin.so

RUN yum -y install iscsi-initiator-utils e2fsprogs xfsprogs && yum clean all

ENTRYPOINT ["/iscsiplugin.sh"]
