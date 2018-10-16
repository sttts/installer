package content

import (
	"text/template"
)

const (
	// BootkubeSystemdContents is a service for running bootkube on the bootstrap
	// nodes
	BootkubeSystemdContents = `
[Unit]
Description=Bootstrap a Kubernetes cluster
Wants=kubelet.service
After=kubelet.service

[Service]
WorkingDirectory=/opt/tectonic

ExecStart=/opt/tectonic/bootkube.sh
# Workaround for https://github.com/opencontainers/runc/pull/1807
ExecStartPost=/usr/bin/touch /opt/tectonic/.bootkube.done

Restart=on-failure
RestartSec=5s
`
)

var (
	// BootkubeShFileTemplate is a script file for running bootkube on the
	// bootstrap nodes.
	BootkubeShFileTemplate = template.Must(template.New("bootkube.sh").Parse(`#!/usr/bin/env bash
set -e

mkdir --parents /etc/kubernetes/{manifests,bootstrap-configs,bootstrap-manifests}

MACHINE_CONFIG_OPERATOR_IMAGE=$(podman run --rm {{.ReleaseImage}} image machine-config-operator)
MACHINE_CONFIG_CONTROLLER_IMAGE=$(podman run --rm {{.ReleaseImage}} image machine-config-controller)
MACHINE_CONFIG_SERVER_IMAGE=$(podman run --rm {{.ReleaseImage}} image machine-config-server)
MACHINE_CONFIG_DAEMON_IMAGE=$(podman run --rm {{.ReleaseImage}} image machine-config-daemon)

KUBE_APISERVER_OPERATOR_IMAGE=$(podman run --rm {{.ReleaseImage}} image cluster-kube-apiserver-operator)
KUBE_CONTROLLER_MANAGER_OPERATOR_IMAGE=$(podman run --rm {{.ReleaseImage}} image cluster-kube-controller-manager-operator)
KUBE_SCHEDULER_OPERATOR_IMAGE=$(podman run --rm {{.ReleaseImage}} image cluster-kube-scheduler-operator)

OPENSHIFT_HYPERSHIFT_IMAGE=$(podman run --rm {{.ReleaseImage}} image hypershift)
OPENSHIFT_HYPERKUBE_IMAGE=$(podman run --rm {{.ReleaseImage}} image hyperkube)

if [ ! -d cvo-bootstrap ]
then
	echo "Rendering Cluster Version Operator Manifests..."

	# shellcheck disable=SC2154
	podman run \
		--volume "$PWD:/assets:z" \
		"{{.ReleaseImage}}" \
		render \
			--output-dir=/assets/cvo-bootstrap \
			--release-image="{{.ReleaseImage}}"

	cp --recursive cvo-bootstrap/manifests .
fi

mkdir --parents ./{bootstrap-manifests,manifests}

if [ ! -d kube-apiserver-bootstrap ]
then
	echo "Rendering Kubernetes API server core manifests..."

	# shellcheck disable=SC2154
	podman run \
		--volume "$PWD:/assets:z" \
		"${KUBE_APISERVER_OPERATOR_IMAGE}" \
		/usr/bin/cluster-kube-apiserver-operator render \
		--manifest-etcd-serving-ca=etcd-client-ca.crt \
		--manifest-etcd-server-urls={{.EtcdCluster}} \
		--manifest-image=${OPENSHIFT_HYPERSHIFT_IMAGE} \
		--asset-input-dir=/assets/tls \
		--asset-output-dir=/assets/kube-apiserver-bootstrap \
		--config-output-file=/assets/kube-apiserver-bootstrap/config

	cp kube-apiserver-bootstrap/config /etc/kubernetes/bootstrap-configs/kube-apiserver-config.yaml
	cp kube-apiserver-bootstrap/bootstrap-manifests/* bootstrap-manifests/
	cp kube-apiserver-bootstrap/manifests/* manifests/
fi

if [ ! -d kube-controller-manager-bootstrap ]
then
	echo "Rendering Kubernetes Controller Manager core manifests..."

	# shellcheck disable=SC2154
	podman run \
		--volume "$PWD:/assets:z" \
		"${KUBE_CONTROLLER_MANAGER_OPERATOR_IMAGE}" \
		/usr/bin/cluster-kube-controller-manager-operator render \
		--manifest-image=${OPENSHIFT_HYPERKUBE_IMAGE} \
		--asset-input-dir=/assets/tls \
		--asset-output-dir=/assets/kube-controller-manager-bootstrap \
		--config-output-file=/assets/kube-controller-manager-bootstrap/config

	cp kube-controller-manager-bootstrap/config /etc/kubernetes/bootstrap-configs/kube-controller-manager-config.yaml
	cp kube-controller-manager-bootstrap/bootstrap-manifests/* bootstrap-manifests/
	cp kube-controller-manager-bootstrap/manifests/* manifests/
fi

if [ ! -d kube-scheduler-bootstrap ]
then
	echo "Rendering Kubernetes Scheduler core manifests..."

	# shellcheck disable=SC2154
	podman run \
		--volume "$PWD:/assets:z" \
		"${KUBE_SCHEDULER_OPERATOR_IMAGE}" \
		/usr/bin/cluster-kube-scheduler-operator render \
		--manifest-image=${OPENSHIFT_HYPERKUBE_IMAGE} \
		--asset-input-dir=/assets/tls \
		--asset-output-dir=/assets/kube-scheduler-bootstrap \
		--config-output-file=/assets/kube-scheduler-bootstrap/config

	cp kube-scheduler-bootstrap/config /etc/kubernetes/bootstrap-configs/kube-scheduler-config.yaml
	cp kube-scheduler-bootstrap/bootstrap-manifests/* bootstrap-manifests/
	cp kube-scheduler-bootstrap/manifests/* manifests/
fi

# fake the not-existing checkpointer operator
cat > manifests/checkpointer-role.yaml <<END
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: pod-checkpointer
  namespace: kube-system
rules:
- apiGroups: [""] # "" indicates the core API group
  resources: ["pods"]
  verbs: ["get", "watch", "list"]
- apiGroups: [""] # "" indicates the core API group
  resources: ["secrets", "configmaps"]
  verbs: ["get"]
END
cat > manifests/checkpointer-role-binding.yaml <<END
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: pod-checkpointer
  namespace: kube-system
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: pod-checkpointer
subjects:
- kind: ServiceAccount
  name: pod-checkpointer
  namespace: kube-system
END
cat > manifests/checkpointer-sa.yaml <<END
apiVersion: v1
kind: ServiceAccount
metadata:
  namespace: kube-system
  name: pod-checkpointer
END
cat > manifests/daemonset-checkpointer.yaml <<END
apiVersion: apps/v1
kind: DaemonSet
metadata:
  labels:
    k8s-app: pod-checkpointer
    tectonic-operators.coreos.com/managed-by: kube-core-operator
    tier: control-plane
  name: pod-checkpointer
  namespace: kube-system
spec:
  selector:
    matchLabels:
      tier: control-plane
      k8s-app: pod-checkpointer
  template:
    metadata:
      annotations:
        scheduler.alpha.kubernetes.io/critical-pod: ""
        checkpointer.alpha.coreos.com/checkpoint: "true"
      labels:
        k8s-app: pod-checkpointer
        tier: control-plane
    spec:
      containers:
      - command:
        - /checkpoint
        - --lock-file=/var/run/lock/pod-checkpointer.lock
        - --kubeconfig=/etc/checkpointer/kubeconfig
        - --checkpoint-grace-period=5m
        - --container-runtime-endpoint=unix:///var/run/crio/crio.sock
        env:
        - name: NODE_NAME
          valueFrom:
            fieldRef:
              fieldPath: spec.nodeName
        - name: POD_NAME
          valueFrom:
            fieldRef:
              fieldPath: metadata.name
        - name: POD_NAMESPACE
          valueFrom:
            fieldRef:
              fieldPath: metadata.namespace
        image: quay.io/coreos/pod-checkpointer:9dc83e1ab3bc36ca25c9f7c18ddef1b91d4a0558
        imagePullPolicy: Always
        name: pod-checkpointer
        securityContext:
          privileged: true
        volumeMounts:
        - mountPath: /etc/checkpointer
          name: kubeconfig
        - mountPath: /etc/kubernetes
          name: etc-kubernetes
        - mountPath: /var/run
          name: var-run
      serviceAccountName: pod-checkpointer
      hostNetwork: true
      nodeSelector:
        node-role.kubernetes.io/master: ""
      restartPolicy: Always
      tolerations:
      - effect: NoSchedule
        key: node-role.kubernetes.io/master
        operator: Exists
      volumes:
      - name: kubeconfig
        secret:
          secretName: controller-manager-kubeconfig
      - hostPath:
          path: /etc/kubernetes
        name: etc-kubernetes
      - hostPath:
          path: /var/run
        name: var-run
  updateStrategy:
    rollingUpdate:
      maxUnavailable: 1
    type: RollingUpdate
END

# fake kube-proxy creation that should be done by the tectonic-network-operator
cat > manifests/kube-system-rbac-role-binding.yaml <<END
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: system:default-sa
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cluster-admin
subjects:
- kind: ServiceAccount
  name: default
  namespace: kube-system
END
cat > manifests/kube-proxy-service-account.yaml <<END
apiVersion: v1
kind: ServiceAccount
metadata:
  namespace: kube-system
  name: kube-proxy
END
cat > manifests/kube-proxy-daemonset.yaml <<END
apiVersion: apps/v1beta2
kind: DaemonSet
metadata:
  labels:
    k8s-app: kube-proxy
    tectonic-operators.coreos.com/managed-by: kube-core-operator
    tier: node
  name: kube-proxy
  namespace: kube-system
spec:
  selector:
    matchLabels:
      k8s-app: kube-proxy
      tier: node
  template:
    metadata:
      labels:
        k8s-app: kube-proxy
        tier: node
    spec:
      containers:
      - command:
        - ./hyperkube
        - proxy
        - --cluster-cidr=10.3.0.0/16
        - --hostname-override=$(NODE_NAME)
        - --kubeconfig=/etc/kubernetes/kubeconfig
        - --proxy-mode=iptables
        env:
        - name: NODE_NAME
          valueFrom:
            fieldRef:
              fieldPath: spec.nodeName
        image: quay.io/coreos/hyperkube:v1.9.3_coreos.0
        name: kube-proxy
        securityContext:
          privileged: true
        volumeMounts:
        - mountPath: /etc/ssl/certs
          name: ssl-certs-host
          readOnly: true
        - mountPath: /etc/kubernetes
          name: kubeconfig
          readOnly: true
      hostNetwork: true
      serviceAccountName: kube-proxy
      tolerations:
      - operator: Exists
      volumes:
      - hostPath:
          path: /etc/ssl/certs
        name: ssl-certs-host
      - name: kubeconfig
        secret:
          defaultMode: 420
          secretName: controller-manager-kubeconfig
  updateStrategy:
    rollingUpdate:
      maxUnavailable: 1
    type: RollingUpdate
END
cat > manifests/kube-proxy-role-binding.yaml <<END
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: kube-proxy
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: system:node-proxier # Automatically created system role.
subjects:
- kind: ServiceAccount
  name: kube-proxy
  namespace: kube-system
END

if [ ! -d mco-bootstrap ]
then
	echo "Rendering MCO manifests..."

	# shellcheck disable=SC2154
	podman run \
		--user 0 \
		--volume "$PWD:/assets:z" \
		"${MACHINE_CONFIG_OPERATOR_IMAGE}" \
		bootstrap \
			--etcd-ca=/assets/tls/etcd-client-ca.crt \
			--root-ca=/assets/tls/root-ca.crt \
			--config-file=/assets/manifests/cluster-config.yaml \
			--dest-dir=/assets/mco-bootstrap \
			--machine-config-controller-image=${MACHINE_CONFIG_CONTROLLER_IMAGE} \
			--machine-config-server-image=${MACHINE_CONFIG_SERVER_IMAGE} \
			--machine-config-daemon-image=${MACHINE_CONFIG_DAEMON_IMAGE} \

	# Bootstrap MachineConfigController uses /etc/mcc/bootstrap/manifests/ dir to
	# 1. read the controller config rendered by MachineConfigOperator
	# 2. read the default MachineConfigPools rendered by MachineConfigOperator
	# 3. read any additional MachineConfigs that are needed for the default MachineConfigPools.
	mkdir --parents /etc/mcc/bootstrap/manifests /etc/kubernetes/manifests/
	cp mco-bootstrap/manifests/* /etc/mcc/bootstrap/manifests/
	cp mco-bootstrap/machineconfigoperator-bootstrap-pod.yaml /etc/kubernetes/manifests/

	# /etc/ssl/mcs/tls.{crt, key} are locations for MachineConfigServer's tls assets.
	mkdir --parents /etc/ssl/mcs/
	cp tls/machine-config-server.crt /etc/ssl/mcs/tls.crt
	cp tls/machine-config-server.key /etc/ssl/mcs/tls.key
fi

# We originally wanted to run the etcd cert signer as
# a static pod, but kubelet could't remove static pod
# when API server is not up, so we have to run this as
# podman container.
# See https://github.com/kubernetes/kubernetes/issues/43292

echo "Starting etcd certificate signer..."

trap "podman rm --force etcd-signer" ERR

# shellcheck disable=SC2154
podman run \
	--name etcd-signer \
	--detach \
	--volume /opt/tectonic/tls:/opt/tectonic/tls:ro,z \
	--network host \
	"{{.EtcdCertSignerImage}}" \
	serve \
	--cacrt=/opt/tectonic/tls/etcd-client-ca.crt \
	--cakey=/opt/tectonic/tls/etcd-client-ca.key \
	--servcrt=/opt/tectonic/tls/apiserver.crt \
	--servkey=/opt/tectonic/tls/apiserver.key \
	--address=0.0.0.0:6443 \
	--csrdir=/tmp \
	--peercertdur=26280h \
	--servercertdur=26280h

echo "Waiting for etcd cluster..."

# Wait for the etcd cluster to come up.
set +e
# shellcheck disable=SC2154,SC2086
until podman run \
		--rm \
		--network host \
		--name etcdctl \
		--env ETCDCTL_API=3 \
		--volume /opt/tectonic/tls:/opt/tectonic/tls:ro,z \
		"{{.EtcdctlImage}}" \
		/usr/local/bin/etcdctl \
		--dial-timeout=10m \
		--cacert=/opt/tectonic/tls/etcd-client-ca.crt \
		--cert=/opt/tectonic/tls/etcd-client.crt \
		--key=/opt/tectonic/tls/etcd-client.key \
		--endpoints={{.EtcdCluster}} \
		endpoint health
do
	echo "etcdctl failed. Retrying in 5 seconds..."
	sleep 5
done
set -e

echo "etcd cluster up. Killing etcd certificate signer..."

podman rm --force etcd-signer
rm --force /etc/kubernetes/manifests/machineconfigoperator-bootstrap-pod.yaml

echo "Starting bootkube..."

# shellcheck disable=SC2154
podman run \
	--rm \
	--volume "$PWD:/assets:z" \
	--volume /etc/kubernetes:/etc/kubernetes:z \
	--network=host \
	--entrypoint=/bootkube \
	"{{.BootkubeImage}}" \
	start --asset-dir=/assets
`))
)
