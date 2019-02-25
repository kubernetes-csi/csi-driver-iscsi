FROM centos:7.4.1708

LABEL maintainers="Kubernetes Authors"
LABEL description="Iscsi Driver"

RUN yum -y install  iscsi-initiator-utils && yum -y install epel-release && yum -y install jq && yum clean all

COPY bin/iscsiplugin /iscsiplugin
ENTRYPOINT ["/iscsiplugin"]


