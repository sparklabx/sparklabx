package services

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

// LocalFileStore is the live view of a user's /workspace/<slug>/local-file while
// their kernel pod runs: the UI reads and writes the pod's own files (like
// `kubectl cp`), so a file uploaded in the browser is usable in the notebook at
// once and the sidecar mirrors it to MinIO. When no pod runs, callers use the
// MinIO copy (local-files/<slug>/) instead.
type LocalFileStore interface {
	// LocalFilesLive reports whether the user's kernel pod is running with local files.
	LocalFilesLive(ctx context.Context, userID string) bool
	LocalList(ctx context.Context, userID, dir string) ([]LocalEntry, error)
	LocalRead(ctx context.Context, userID, file string) ([]byte, error)
	LocalWrite(ctx context.Context, userID, file string, data []byte) error
	LocalMkdir(ctx context.Context, userID, dir string) error
	LocalDelete(ctx context.Context, userID, p string) error
}

// LocalEntry is one directory entry under local-file/.
type LocalEntry struct {
	Name    string
	Size    int64
	IsDir   bool
	ModTime time.Time
}

const kernelContainer = "jupyter-kernel"

// LocalFilesIgnored are names under local-file/ that are neither mirrored to MinIO nor
// listed in the UI: in-flight uploads, and scratch Spark/Jupyter write to the working
// directory (which is local-file/). Patterns use mc --exclude / path.Match syntax.
var LocalFilesIgnored = []string{"*.uploading", "spark-warehouse", "spark-warehouse/*", "metastore_db", "metastore_db/*", "derby.log", ".ipynb_checkpoints", ".ipynb_checkpoints/*"}

// IsLocalFileIgnored reports whether a directory entry name is hidden from the UI.
func IsLocalFileIgnored(name string) bool {
	for _, p := range LocalFilesIgnored {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

// localRoot returns the pod name and its local-file root ("/workspace/<slug>/local-file")
// if the pod is running and ready with local files enabled.
func (g *K8sPerUserGateway) localRoot(ctx context.Context, userID string) (string, string, bool) {
	name := podNameForUser(userID)
	pod, err := g.client.CoreV1().Pods(g.cfg.Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil || pod.Status.Phase != corev1.PodRunning || pod.DeletionTimestamp != nil {
		return "", "", false
	}
	for _, c := range pod.Spec.Containers {
		if c.Name == kernelContainer && c.WorkingDir != "" {
			for _, s := range pod.Status.ContainerStatuses {
				if s.Name == kernelContainer && s.Ready {
					root := c.WorkingDir // = local-file (older pods: its parent)
					if !strings.HasSuffix(root, "/local-file") {
						root += "/local-file"
					}
					return name, root, true
				}
			}
		}
	}
	return "", "", false
}

func (g *K8sPerUserGateway) LocalFilesLive(ctx context.Context, userID string) bool {
	_, _, ok := g.localRoot(ctx, userID)
	return ok
}

// localPath maps a UI-relative path into the root; anything escaping it is rejected.
func localPath(root, rel string) (string, error) {
	clean := path.Clean("/" + rel)
	if strings.Contains(rel, "\x00") {
		return "", fmt.Errorf("invalid path")
	}
	if clean == "/" {
		return root, nil
	}
	return root + clean, nil // clean starts with "/" and has no ".." left
}

// execIn runs argv (never through a shell with user input in the script) in the
// kernel container. User paths are passed as separate arguments.
func (g *K8sPerUserGateway) execIn(ctx context.Context, pod string, argv []string, stdin io.Reader, stdout io.Writer) error {
	req := g.client.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(g.cfg.Namespace).Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: kernelContainer, Command: argv,
			Stdin: stdin != nil, Stdout: stdout != nil, Stderr: true,
		}, scheme.ParameterCodec)
	ex, err := remotecommand.NewSPDYExecutor(g.restCfg, "POST", req.URL())
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	if err := ex.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: stdin, Stdout: stdout, Stderr: &stderr}); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (g *K8sPerUserGateway) live(ctx context.Context, userID, rel string) (pod, abs string, err error) {
	pod, root, ok := g.localRoot(ctx, userID)
	if !ok {
		return "", "", fmt.Errorf("kernel not running")
	}
	abs, err = localPath(root, rel)
	return pod, abs, err
}

func (g *K8sPerUserGateway) LocalList(ctx context.Context, userID, dir string) ([]LocalEntry, error) {
	pod, abs, err := g.live(ctx, userID, dir)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	// One level, tab-separated: type, size, mtime (epoch), name. Missing dir -> empty.
	script := `[ -d "$1" ] || exit 0; find "$1" -mindepth 1 -maxdepth 1 -printf '%y\t%s\t%T@\t%f\n'`
	if err := g.execIn(ctx, pod, []string{"sh", "-c", script, "_", abs}, nil, &out); err != nil {
		return nil, err
	}
	return parseFindOutput(out.String()), nil
}

func parseFindOutput(s string) []LocalEntry {
	entries := []LocalEntry{}
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		f := strings.SplitN(line, "\t", 4)
		if len(f) != 4 {
			continue
		}
		size, _ := strconv.ParseInt(f[1], 10, 64)
		secs, _ := strconv.ParseFloat(f[2], 64)
		entries = append(entries, LocalEntry{
			Name: f[3], Size: size, IsDir: f[0] == "d",
			ModTime: time.Unix(int64(secs), 0).UTC(),
		})
	}
	return entries
}

func (g *K8sPerUserGateway) LocalRead(ctx context.Context, userID, file string) ([]byte, error) {
	pod, abs, err := g.live(ctx, userID, file)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := g.execIn(ctx, pod, []string{"cat", "--", abs}, nil, &out); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func (g *K8sPerUserGateway) LocalWrite(ctx context.Context, userID, file string, data []byte) error {
	pod, abs, err := g.live(ctx, userID, file)
	if err != nil {
		return err
	}
	// Write to a temp name then rename, so the sidecar never mirrors a half-written file.
	script := `mkdir -p "$(dirname "$1")" && cat > "$1.uploading" && mv -f "$1.uploading" "$1"`
	return g.execIn(ctx, pod, []string{"sh", "-c", script, "_", abs}, bytes.NewReader(data), nil)
}

func (g *K8sPerUserGateway) LocalMkdir(ctx context.Context, userID, dir string) error {
	pod, abs, err := g.live(ctx, userID, dir)
	if err != nil {
		return err
	}
	return g.execIn(ctx, pod, []string{"mkdir", "-p", "--", abs}, nil, nil)
}

func (g *K8sPerUserGateway) LocalDelete(ctx context.Context, userID, p string) error {
	pod, root, ok := g.localRoot(ctx, userID)
	if !ok {
		return fmt.Errorf("kernel not running")
	}
	abs, err := localPath(root, p)
	if err != nil {
		return err
	}
	if abs == root {
		return fmt.Errorf("refusing to delete the local-file root")
	}
	return g.execIn(ctx, pod, []string{"rm", "-rf", "--", abs}, nil, nil)
}
