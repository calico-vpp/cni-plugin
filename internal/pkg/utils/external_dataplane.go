package utils

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/cni/pkg/types/current"
	"github.com/projectcalico/cni-plugin/pkg/types"
	"github.com/projectcalico/cni-plugin/proto"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
)

const (
	timeout = 5 * time.Second
)

func doExternalNetworking(
	args *skel.CmdArgs,
	conf types.NetConf,
	result *current.Result,
	logger *logrus.Entry,
	desiredVethName string,
	routes []*net.IPNet,
) (ifName, contTapMAC string, err error) {
	logger.Infof("Creating container interface using external networking")

	address, ok := conf.DataplaneOptions["socket"].(string)
	if !ok {
		return "", "", fmt.Errorf("external dataplane socket not configured")
	}

	logger.Infof("Connecting to CNI backend server at %s", address)
	conn, err := grpc.Dial(address, grpc.WithInsecure())
	if err != nil {
		return "", "", fmt.Errorf("cannot connect to external dataplane: %v", err)
	}
	c := proto.NewCniDataplaneClient(conn)

	request := &proto.AddRequest{
		InterfaceName:            args.IfName,
		Netns:                    args.Netns,
		DesiredHostInterfaceName: desiredVethName,
		Settings: &proto.ContainerSettings{
			AllowIpForwarding: conf.ContainerSettings.AllowIPForwarding,
		},
		ContainerIps:    make([]*proto.IPConfig, 0),
		ContainerRoutes: make([]*proto.IPNet, 0),
	}
	for _, ipAddr := range result.IPs {
		IsIPv6 := ipAddr.Version != "4"
		IP := ipAddr.Address.IP.To4()
		if IP == nil {
			IP = ipAddr.Address.IP.To16()
		}
		gwIP := ipAddr.Gateway.To4()
		if gwIP == nil {
			gwIP = ipAddr.Gateway.To16()
		}
		prefLen, _ := ipAddr.Address.Mask.Size()
		request.ContainerIps = append(request.ContainerIps, &proto.IPConfig{
			Ip: &proto.IPNet{
				Ip: &proto.IP{
					IsIpv6: IsIPv6,
					Ip:     IP,
				},
				PrefixLen: int32(prefLen),
			},
			Gateway: &proto.IP{
				IsIpv6: IsIPv6,
				Ip:     gwIP,
			},
		})
	}
	for _, r := range routes {
		IP := r.IP.To4()
		if IP == nil {
			IP = r.IP.To16()
		}
		prefLen, _ := r.Mask.Size()
		request.ContainerRoutes = append(request.ContainerRoutes, &proto.IPNet{
			Ip: &proto.IP{
				IsIpv6: len(IP) == 16,
				Ip:     IP,
			},
			PrefixLen: int32(prefLen),
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	reply, err := c.Add(ctx, request)
	if err != nil {
		logger.Errorf("request to external dataplane failed : %v", err)
		return "", "", err
	}
	if !reply.GetSuccessful() {
		return reply.GetInterfaceName(), reply.GetContainerMac(), fmt.Errorf("external dataplane error: %s", reply.GetErrorMessage())
	}
	return reply.GetInterfaceName(), reply.GetContainerMac(), nil
}

func cleanUpExternalNetworking(args *skel.CmdArgs, conf types.NetConf, logger *logrus.Entry) error {
	logger.Infof("Deleting container interface using external networking")

	address, ok := conf.DataplaneOptions["socket"].(string)
	if !ok {
		return fmt.Errorf("external dataplane socket not configured")
	}

	logger.Infof("Connecting to CNI backend server at %s", address)
	conn, err := grpc.Dial(address, grpc.WithInsecure())
	if err != nil {
		return fmt.Errorf("cannot connect to external dataplane: %v", err)
	}
	c := proto.NewCniDataplaneClient(conn)

	request := &proto.DelRequest{
		InterfaceName: args.IfName,
		Netns:         args.Netns,
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	reply, err := c.Del(ctx, request)
	if err != nil {
		logger.Errorf("request to external dataplane failed : %v", err)
		return err
	}
	if !reply.GetSuccessful() {
		return fmt.Errorf("external dataplane error: %s", reply.GetErrorMessage())
	}
	return nil
}
