package consul

import (
	"fmt"
	"log"
	"net"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"github.com/hashicorp/consul/api"
)

type ConsulConfig struct {
	TagPrefix string
	Statuses []string
	UseServiceName bool
}

func watchServices(client *api.Client, consulConfig *ConsulConfig, config chan string) {
	var lastIndex uint64

	for {
		q := &api.QueryOptions{RequireConsistent: true, WaitIndex: lastIndex}
		checks, meta, err := client.Health().State("any", q)
		if err != nil {
			log.Printf("[WARN] consul: Error fetching health state. %v", err)
			time.Sleep(time.Second)
			continue
		}

		log.Printf("[INFO] consul: Health changed to #%d", meta.LastIndex)
		config <- servicesConfig(client, passingServices(checks, consulConfig.Statuses), consulConfig)
		lastIndex = meta.LastIndex
	}
}

// servicesConfig determines which service instances have passing health checks
// and then finds the ones which have tags with the right prefix to build the config from.
func servicesConfig(client *api.Client, checks []*api.HealthCheck, consulConfig *ConsulConfig) string {
	// map service name to list of service passing for which the health check is ok
	m := map[string]map[string]bool{}
	for _, check := range checks {
		name, id := check.ServiceName, check.ServiceID

		if _, ok := m[name]; !ok {
			m[name] = map[string]bool{}
		}
		m[name][id] = true
	}

	var config []string
	for name, passing := range m {
		cfg := serviceConfig(client, name, passing, consulConfig)
		config = append(config, cfg...)
	}

	// sort config in reverse order to sort most specific config to the top
	sort.Sort(sort.Reverse(sort.StringSlice(config)))

	return strings.Join(config, "\n")
}

// serviceConfig constructs the config for all good instances of a single service.
func serviceConfig(client *api.Client, name string, passing map[string]bool, consulConfig *ConsulConfig) (config []string) {
	if name == "" || len(passing) == 0 {
		return nil
	}

	dc, err := datacenter(client)
	if err != nil {
		log.Printf("[WARN] consul: Error getting datacenter. %s", err)
		return nil
	}

	q := &api.QueryOptions{RequireConsistent: true}
	svcs, _, err := client.Catalog().Service(name, "", q)
	if err != nil {
		log.Printf("[WARN] consul: Error getting catalog service %s. %v", name, err)
		return nil
	}

	env := map[string]string{
		"DC": dc,
	}

	for _, svc := range svcs {
		// check if the instance is in the list of instances
		// which passed the health check
		if _, ok := passing[svc.ServiceID]; !ok {
			continue
		}

		// if registry.consul.register.byServiceName option from config is enabled
		if consulConfig.UseServiceName {
			name, addr, port := serviceDestination(svc)
			addrport := net.JoinHostPort(addr, strconv.Itoa(port))
			config = append(config, fmt.Sprintf("route add %s %s%s http://%s/ tags %q", name, "", "/" + name, addrport, strings.Join(svc.ServiceTags, ",")))
		} else {
			for _, tag := range svc.ServiceTags {
				if host, path, ok := parseURLPrefixTag(tag, consulConfig.TagPrefix, env); ok {
					name, addr, port := serviceDestination(svc)
					addrport := net.JoinHostPort(addr, strconv.Itoa(port))
					config = append(config, fmt.Sprintf("route add %s %s%s http://%s/ tags %q", name, host, path, addrport, strings.Join(svc.ServiceTags, ",")))
				}
			}

		}

	}
	return config
}

func serviceDestination(svc *api.CatalogService) (string, string, int) {
	name, addr, port := svc.ServiceName, svc.ServiceAddress, svc.ServicePort
	if addr == "" {
		addr = svc.Address
	}

	// add .local suffix on OSX for simple host names w/o domain
	if runtime.GOOS == "darwin" && !strings.Contains(addr, ".") && !strings.HasSuffix(addr, ".local") {
		addr += ".local"
	}
	return name, addr, port
}