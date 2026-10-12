package services

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestLocalFilesPodSpec(t *testing.T) {
	g := &K8sPerUserGateway{cfg: K8sPerUserConfig{
		Namespace: "ns", PodImage: "kernel", SyncImage: "mc", Bucket: "workspace", LocalFilesSize: "5Gi",
		CPURequest: "500m", MemoryRequest: "1Gi", CPULimit: "2", MemoryLimit: "4Gi",
		CredsResolver: func(string) (string, string, error) { return "alice", "secret", nil },
	}}
	pod := g.buildPodSpec("user-1", "kernel-x", g.resolveSpec(nil))

	k := pod.Spec.Containers[0]
	if k.WorkingDir != "/workspace/alice" || len(k.VolumeMounts) != 1 || k.VolumeMounts[0].MountPath != "/workspace" {
		t.Fatalf("kernel workdir/mount: %q %+v", k.WorkingDir, k.VolumeMounts)
	}
	if len(pod.Spec.InitContainers) != 2 {
		t.Fatalf("want restore + sync init containers, got %d", len(pod.Spec.InitContainers))
	}
	restore, sync := pod.Spec.InitContainers[0], pod.Spec.InitContainers[1]
	rc := restore.Command[2]
	if !strings.Contains(rc, "mc ls s3/workspace/local-files/alice/") || strings.Contains(rc, "|| true") {
		t.Fatalf("restore must fail on MinIO errors and skip only an empty prefix: %s", rc)
	}
	if sync.RestartPolicy == nil || *sync.RestartPolicy != corev1.ContainerRestartPolicyAlways {
		t.Fatal("sync must be a native sidecar (restartPolicy Always)")
	}
	if !strings.Contains(sync.Command[2], "mc mirror --overwrite --remove /workspace/alice/local-file/ s3/workspace/local-files/alice/ --watch") {
		t.Fatalf("sync command: %s", sync.Command[2])
	}
	if sync.Lifecycle == nil || sync.Lifecycle.PreStop == nil {
		t.Fatal("sync needs a preStop final mirror")
	}
	var foundCreds bool
	for _, e := range sync.Env {
		foundCreds = foundCreds || (e.Name == "AWS_ACCESS_KEY_ID" && e.Value == "alice")
	}
	if !foundCreds {
		t.Fatal("sync must use the user's own MinIO creds")
	}
	if v := pod.Spec.Volumes[0].EmptyDir; v == nil || v.SizeLimit == nil || v.SizeLimit.String() != "5Gi" {
		t.Fatalf("emptyDir sizeLimit: %+v", pod.Spec.Volumes)
	}

	// Root-cred fallback (no per-user IAM): no local files, nothing else changes.
	g.cfg.CredsResolver = nil
	if p := g.buildPodSpec("user-1", "kernel-x", g.resolveSpec(nil)); len(p.Spec.InitContainers) != 0 || p.Spec.Containers[0].WorkingDir != "" {
		t.Fatal("local files must be off without per-user creds")
	}
	// Feature off.
	g.cfg.CredsResolver = func(string) (string, string, error) { return "alice", "secret", nil }
	g.cfg.SyncImage = ""
	if p := g.buildPodSpec("user-1", "kernel-x", g.resolveSpec(nil)); len(p.Spec.InitContainers) != 0 {
		t.Fatal("empty SyncImage must disable local files")
	}
}

func TestUserPolicyCoversLocalFiles(t *testing.T) {
	b, err := buildUserPolicy("workspace", "alice")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"arn:aws:s3:::workspace/local-files/alice/*", `"local-files/alice/*"`, "arn:aws:s3:::workspace/users/alice/*"} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("policy missing %s: %s", want, b)
		}
	}
	if strings.Contains(string(b), "local-files/*") {
		t.Fatal("policy must not grant other users' local files")
	}
}
