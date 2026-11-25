// fuse-device-plugin offers /dev/fuse to pods as the resource syeyoung.dev/fuse, for containers with "FUSE mounts" on: a pod that requests one unit gets
// the host's /dev/fuse node, nothing else. It never touches the host beyond the kubelet's
// device-plugin socket directory.
//
// When the kubelet restarts it deletes the plugins' sockets: this plugin sees its own go (a file
// event, not polling) and exits, so the pod restarts and registers again.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

type plugin struct {
	pluginapi.UnimplementedDevicePluginServer
	path  string
	count int
}

func (p *plugin) GetDevicePluginOptions(context.Context, *pluginapi.Empty) (*pluginapi.DevicePluginOptions, error) {
	return &pluginapi.DevicePluginOptions{}, nil
}

func (p *plugin) ListAndWatch(_ *pluginapi.Empty, s pluginapi.DevicePlugin_ListAndWatchServer) error {
	devs := make([]*pluginapi.Device, p.count)
	for i := range devs {
		devs[i] = &pluginapi.Device{ID: fmt.Sprintf("fuse-%d", i), Health: pluginapi.Healthy}
	}
	if err := s.Send(&pluginapi.ListAndWatchResponse{Devices: devs}); err != nil {
		return err
	}
	<-s.Context().Done()
	return nil
}

func (p *plugin) Allocate(_ context.Context, r *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	resp := &pluginapi.AllocateResponse{}
	for range r.ContainerRequests {
		// However many units a container asked for, it gets the one node.
		resp.ContainerResponses = append(resp.ContainerResponses, &pluginapi.ContainerAllocateResponse{
			Devices: []*pluginapi.DeviceSpec{{ContainerPath: p.path, HostPath: p.path, Permissions: "rw"}},
		})
	}
	return resp, nil
}

func main() {
	dir := flag.String("dir", pluginapi.DevicePluginPath, "the kubelet's device-plugin directory")
	resource := flag.String("resource", "syeyoung.dev/fuse", "the resource name")
	path := flag.String("path", "/dev/fuse", "the device node")
	// /dev/fuse is shared, not scarce, but device plugins only count units: as many as the node can
	// have pods (the kubelet's maxPods, cluster/k0s.yaml), so the count never refuses one.
	count := flag.Int("count", 1000, "units offered: the node's maxPods")
	flag.Parse()
	if _, err := os.Stat(*path); err != nil {
		log.Fatalf("%s: %v", *path, err)
	}
	socket := "containerservice-fuse.sock"
	sockPath := filepath.Join(*dir, socket)
	_ = os.Remove(sockPath)
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}
	if err := watcher.Add(*dir); err != nil {
		log.Fatal(err)
	}
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		log.Fatal(err)
	}
	srv := grpc.NewServer()
	pluginapi.RegisterDevicePluginServer(srv, &plugin{path: *path, count: *count})
	go func() { log.Fatal(srv.Serve(l)) }()

	conn, err := grpc.NewClient("unix://"+filepath.Join(*dir, "kubelet.sock"), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	_, err = pluginapi.NewRegistrationClient(conn).Register(ctx, &pluginapi.RegisterRequest{
		Version: pluginapi.Version, Endpoint: socket, ResourceName: *resource,
	})
	cancel()
	conn.Close()
	if err != nil {
		log.Fatalf("registering %s: %v", *resource, err)
	}
	log.Printf("registered %s: %d units of %s", *resource, *count, *path)
	for {
		select {
		case ev := <-watcher.Events:
			if ev.Name == sockPath && ev.Has(fsnotify.Remove) {
				log.Fatalf("%s is gone (the kubelet restarted?): exiting to register again", sockPath)
			}
		case err := <-watcher.Errors:
			log.Fatalf("watching %s: %v", *dir, err)
		}
	}
}
