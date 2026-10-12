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
	if !strings.Contains(sync.Command[2], "mc mirror --overwrite --remove --exclude '*.uploading' /workspace/alice/local-file/ s3/workspace/local-files/alice/ --watch") {
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
	if strings.Contains(string(b), "public") {
		t.Fatal("the shared public/ space was removed: no grant on it")
	}
}

func TestLocalPathStaysInRoot(t *testing.T) {
	root := "/workspace/alice/local-file"
	for rel, want := range map[string]string{
		"":                 root,
		"/":                root,
		"data/x.csv":       root + "/data/x.csv",
		"data/":            root + "/data",
		"../../etc/passwd": root + "/etc/passwd",
		"a/../../../bob/s": root + "/bob/s",
		"./notes.txt":      root + "/notes.txt",
	} {
		got, err := localPath(root, rel)
		if err != nil || got != want {
			t.Fatalf("localPath(%q) = %q, %v; want %q", rel, got, err, want)
		}
	}
	if _, err := localPath(root, "a\x00b"); err == nil {
		t.Fatal("NUL byte must be rejected")
	}
}

func TestParseFindOutput(t *testing.T) {
	got := parseFindOutput("d\t4096\t1700000000.5\tdata\nf\t12\t1700000001.0\tnote.txt\n")
	if len(got) != 2 || !got[0].IsDir || got[0].Name != "data" || got[1].IsDir || got[1].Size != 12 || got[1].ModTime.Unix() != 1700000001 {
		t.Fatalf("parse: %+v", got)
	}
	if len(parseFindOutput("")) != 0 {
		t.Fatal("empty output must give no entries")
	}
}
