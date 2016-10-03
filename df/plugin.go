// +build linux

/*
http://www.apache.org/licenses/LICENSE-2.0.txt


Copyright 2015 Intel Corporation

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package df

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"

	log "github.com/Sirupsen/logrus"

	"github.com/intelsdi-x/snap/control/plugin"
	"github.com/intelsdi-x/snap/control/plugin/cpolicy"
	"github.com/intelsdi-x/snap/core"

	"github.com/intelsdi-x/snap-plugin-utilities/config"
)

const (
	// PluginName df collector plugin name
	PluginName = "df"
	// Version of plugin
	Version = 4

	nsVendor = "intel"
	nsClass  = "procfs"
	nsType   = "filesystem"
)

var (
	//procPath source of data for metrics
	procPath = "/proc"
	// prefix in metric namespace
	namespacePrefix = []string{nsVendor, nsClass, nsType}
	metricsKind     = []string{
		"space_free",
		"space_reserved",
		"space_used",
		"space_percent_free",
		"space_percent_reserved",
		"space_percent_used",
		"inodes_free",
		"inodes_reserved",
		"inodes_used",
		"inodes_percent_free",
		"inodes_percent_reserved",
		"inodes_percent_used",
		"device_name",
		"device_type",
	}
	dfltExcludedFSNames = []string{
		"/proc/sys/fs/binfmt_misc",
		"/var/lib/docker/aufs",
	}
	dfltExcludedFSTypes = []string{
		"proc",
		"binfmt_misc",
		"fuse.gvfsd-fuse",
		"sysfs",
		"cgroup",
		"fusectl",
		"pstore",
		"debugfs",
		"securityfs",
		"devpts",
		"mqueue",
		"hugetlbfs",
		"nsfs",
		"rpc_pipefs",
		"devtmpfs",
		"none",
		"tmpfs",
		"aufs",
	}
)

// Function to check properness of configuration parameter
// and set plugin attribute accordingly
func (p *dfCollector) setProcPath(cfg interface{}) error {
	p.initializedMutex.Lock()
	defer p.initializedMutex.Unlock()
	if p.initialized {
		return nil
	}
	procPath, err := config.GetConfigItem(cfg, "proc_path")
	if err == nil && len(procPath.(string)) > 0 {
		procPathStats, err := os.Stat(procPath.(string))
		if err != nil {
			return err
		}
		if !procPathStats.IsDir() {
			return errors.New(fmt.Sprintf("%s is not a directory", procPath.(string)))
		}
		p.proc_path = procPath.(string)
	}
	excludedFSNames, err := config.GetConfigItem(cfg, "excluded_fs_names")
	if err == nil {
		if len(excludedFSNames.(string)) > 0 {
			p.excluded_fs_names = strings.Split(excludedFSNames.(string), ",")
		} else {
			p.excluded_fs_names = []string{}
		}
	} else {
		p.excluded_fs_names = dfltExcludedFSNames
	}
	excludedFSTypes, err := config.GetConfigItem(cfg, "excluded_fs_types")
	if err == nil {
		if len(excludedFSTypes.(string)) > 0 {
			p.excluded_fs_types = strings.Split(excludedFSTypes.(string), ",")
		} else {
			p.excluded_fs_types = []string{}
		}
	} else {
		p.excluded_fs_types = dfltExcludedFSTypes
	}
	p.initialized = true
	return nil
}

// GetMetricTypes returns list of available metric types
// It returns error in case retrieval was not successful
func (p *dfCollector) GetMetricTypes(cfg plugin.ConfigType) ([]plugin.MetricType, error) {
	mts := []plugin.MetricType{}
	for _, kind := range metricsKind {
		mts = append(mts, plugin.MetricType{
			Namespace_: core.NewNamespace(namespacePrefix...).
				AddDynamicElement(nsType, "name of filesystem").
				AddStaticElement(kind),
			Description_: "dynamic filesystem metric: " + kind,
		})
	}
	return mts, nil
}

// CollectMetrics returns list of requested metric values
// It returns error in case retrieval was not successful
func (p *dfCollector) CollectMetrics(mts []plugin.MetricType) ([]plugin.MetricType, error) {
	err := p.setProcPath(mts[0])
	if err != nil {
		return nil, err
	}
	metrics := []plugin.MetricType{}
	curTime := time.Now()
	dfms, err := p.stats.collect(p.proc_path, p.excluded_fs_names, p.excluded_fs_types)
	if err != nil {
		return metrics, fmt.Errorf(fmt.Sprintf("Unable to collect metrics from df: %s", err))
	}
	for _, m := range mts {
		ns := m.Namespace()
		lns := len(ns)
		if lns < 4 {
			return nil, fmt.Errorf("Wrong namespace length %d: should be at least 4", lns)
		}
		// We can request all metrics for all devices in one shot
		// using namespace /intel/procfs/filesystem/*
		if lns == 4 {
			if ns[lns-1].Value != "*" {
				return nil, fmt.Errorf("Namespace should contain wildcard")
			}
			for _, kind := range metricsKind {
				for _, dfm := range dfms {
					metric := createMetric(
						core.NewNamespace(
							createNamespace(dfm.MountPoint, kind)...),
						curTime)
					fillMetric(kind, dfm, &metric)
					metrics = append(metrics, metric)
				}
			}
		} else if ns[lns-2].Value == "*" {
			// namespace /intel/procfs/filesystem/*/<metric>
			kind := ns[lns-1].Value
			// <metric> is also wildcard => get them all
			if kind == "*" {
				for _, skind := range metricsKind {
					for _, dfm := range dfms {
						metric := createMetric(
							core.NewNamespace(
								createNamespace(dfm.MountPoint, skind)...),
							curTime)
						fillMetric(skind, dfm, &metric)
						metrics = append(metrics, metric)
					}
				}
			} else {
				// <metric> is not wildcard => getonly matching metrics
				for _, dfm := range dfms {
					metric := createMetric(
						core.NewNamespace(
							createNamespace(dfm.MountPoint, kind)...),
						curTime)
					fillMetric(kind, dfm, &metric)
					metrics = append(metrics, metric)
				}
			}
		} else {
			// namespace /intel/procfs/filesystem/<fs>/<metric>
			kind := ns[lns-1].Value
			// <metric> is also wildcard => get them all
			if kind == "*" {
				for _, skind := range metricsKind {
					for _, dfm := range dfms {
						if ns[lns-2].Value == dfm.MountPoint {
							metric := createMetric(
								core.NewNamespace(
									createNamespace(dfm.MountPoint, skind)...),
								curTime)
							fillMetric(skind, dfm, &metric)
							metrics = append(metrics, metric)
						}
					}
				}
			} else {
				for _, dfm := range dfms {
					if ns[lns-2].Value == dfm.MountPoint {
						metric := createMetric(ns, curTime)
						fillMetric(kind, dfm, &metric)
						metrics = append(metrics, metric)
					}
				}
			}
		}
	}
	return metrics, nil
}

func createMetric(ns core.Namespace, curTime time.Time) plugin.MetricType {
	metric := plugin.MetricType{
		Timestamp_: curTime,
		Namespace_: ns,
	}
	ns[len(ns)-2].Name = nsType
	return metric
}

// Function to fill metric with proper (computed) value
func fillMetric(kind string, dfm dfMetric, metric *plugin.MetricType) {
	switch kind {
	case "space_free":
		metric.Data_ = dfm.Available
	case "space_reserved":
		metric.Data_ = dfm.Blocks - (dfm.Used + dfm.Available)
	case "space_used":
		metric.Data_ = dfm.Used
	case "space_percent_free":
		metric.Data_ = 100 * float64(dfm.Available) / float64(dfm.Blocks)
	case "space_percent_reserved":
		metric.Data_ = 100 * float64(dfm.Blocks-(dfm.Used+dfm.Available)) / float64(dfm.Blocks)
	case "space_percent_used":
		metric.Data_ = 100 * float64(dfm.Used) / float64(dfm.Blocks)
	case "device_name":
		metric.Data_ = dfm.Filesystem
	case "device_type":
		metric.Data_ = dfm.FsType
	case "inodes_free":
		metric.Data_ = dfm.IFree
	case "inodes_reserved":
		metric.Data_ = dfm.Inodes - (dfm.IUsed + dfm.IFree)
	case "inodes_used":
		metric.Data_ = dfm.IUsed
	case "inodes_percent_free":
		metric.Data_ = 100 * float64(dfm.IFree) / float64(dfm.Inodes)
	case "inodes_percent_reserved":
		metric.Data_ = 100 * float64(dfm.Inodes-(dfm.IUsed+dfm.IFree)) / float64(dfm.Inodes)
	case "inodes_percent_used":
		metric.Data_ = 100 * float64(dfm.IUsed) / float64(dfm.Inodes)
	}
}

// createNamespace returns namespace slice of strings composed from: vendor, class, type and components of metric name
func createNamespace(elt string, name string) []string {
	var suffix = []string{elt, name}
	return append(namespacePrefix, suffix...)
}

// GetConfigPolicy returns config policy
// It returns error in case retrieval was not successful
func (p *dfCollector) GetConfigPolicy() (*cpolicy.ConfigPolicy, error) {
	cp := cpolicy.New()
	rule, _ := cpolicy.NewStringRule("proc_path", false, "/proc")
	node := cpolicy.NewPolicyNode()
	node.Add(rule)
	cp.Add([]string{nsVendor, nsClass, PluginName}, node)
	rule, _ = cpolicy.NewStringRule("excluded_fs_names", false, strings.Join(dfltExcludedFSNames, ","))
	node.Add(rule)
	rule, _ = cpolicy.NewStringRule("excluded_fs_types", false, strings.Join(dfltExcludedFSTypes, ","))
	node.Add(rule)
	return cp, nil
}

// NewDfCollector creates new instance of plugin and returns pointer to initialized object.
func NewDfCollector() *dfCollector {
	logger := log.New()
	imutex := new(sync.Mutex)
	return &dfCollector{
		stats:             &dfStats{},
		logger:            logger,
		initializedMutex:  imutex,
		proc_path:         procPath,
		excluded_fs_names: dfltExcludedFSNames,
		excluded_fs_types: dfltExcludedFSTypes,
	}
}

// Meta returns plugin's metadata
func Meta() *plugin.PluginMeta {
	return plugin.NewPluginMeta(
		PluginName,
		Version,
		plugin.CollectorPluginType,
		[]string{plugin.SnapGOBContentType},
		[]string{plugin.SnapGOBContentType},
		plugin.ConcurrencyCount(1),
	)
}

type dfCollector struct {
	initialized       bool
	initializedMutex  *sync.Mutex
	stats             collector
	logger            *log.Logger
	proc_path         string
	excluded_fs_names []string
	excluded_fs_types []string
}

type dfMetric struct {
	Filesystem              string
	Used, Available, Blocks uint64
	Capacity                float64
	FsType                  string
	MountPoint              string
	UnchangedMountPoint     string
	Inodes, IUsed, IFree    uint64
	IUse                    float64
}

type collector interface {
	collect(string, []string, []string) ([]dfMetric, error)
}

type dfStats struct{}

func (dfs *dfStats) collect(procPath string, excluded_fs_names []string, excluded_fs_types []string) ([]dfMetric, error) {
	dfms := []dfMetric{}
	cpath := path.Join(procPath, "1", "mountinfo")
	fh, err := os.Open(cpath)
	if err != nil {
		log.Error(fmt.Sprintf("Got error %#v", err))
		return nil, err
	}
	defer fh.Close()
	scanner := bufio.NewScanner(fh)
	for scanner.Scan() {
		inLine := scanner.Text()
		// https://www.kernel.org/doc/Documentation/filesystems/proc.txt
		// or "man proc" + look for mountinfo to see meaning of fields
		lParts := strings.Split(inLine, " - ")
		if len(lParts) != 2 {
			return nil, fmt.Errorf("Wrong format %d parts found instead of 2", len(lParts))
		}
		leftFields := strings.Fields(lParts[0])
		if len(leftFields) != 6 && len(leftFields) != 7 {
			return nil, fmt.Errorf("Wrong format %d fields found on the left side instead of 6 or 7", len(leftFields))
		}
		rightFields := strings.Fields(lParts[1])
		if len(rightFields) != 3 {
			return nil, fmt.Errorf("Wrong format %d fields found on the right side instead of 7 min", len(rightFields))
		}
		// Keep only meaningfull filesystems
		if excludedFSFromList(leftFields[4], excluded_fs_names) {
			log.Debug(fmt.Sprintf("Ignoring mount point %s",
				leftFields[4]))
			continue
		}
		if excludedFSFromList(rightFields[0], excluded_fs_types) {
			log.Debug(fmt.Sprintf("Ignoring mount point %s with FS type %s",
				leftFields[4], rightFields[0]))
			continue
		}
		var dfm dfMetric
		dfm.Filesystem = rightFields[1]
		dfm.FsType = rightFields[0]
		dfm.UnchangedMountPoint = leftFields[4]
		if leftFields[4] == "/" {
			dfm.MountPoint = "rootfs"
		} else {
			dfm.MountPoint = strings.Replace(leftFields[4][1:], "/", "_", -1)
			// Because there are mounted FS containing dots
			// (like /etc/resolv.conf in Docker containers)
			// and this is incompatible with Snap metric name policies
			dfm.MountPoint = strings.Replace(dfm.MountPoint, ".", "_", -1)
		}
		stat := syscall.Statfs_t{}
		err := syscall.Statfs(leftFields[4], &stat)
		if err != nil {
			log.Error(fmt.Sprintf("Error getting filesystem infos for %s", leftFields[4]))
			continue
		}
		// Blocks
		dfm.Blocks = (stat.Blocks * uint64(stat.Bsize)) / 1024
		dfm.Available = (stat.Bavail * uint64(stat.Bsize)) / 1024
		xFree := (stat.Bfree * uint64(stat.Bsize)) / 1024
		dfm.Used = dfm.Blocks - xFree
		percentAvailable := ceilPercent(dfm.Used, dfm.Used+dfm.Available)
		dfm.Capacity = percentAvailable / 100.0
		// Inodes
		dfm.Inodes = stat.Files
		dfm.IFree = stat.Ffree
		dfm.IUsed = dfm.Inodes - dfm.IFree
		percentIUsed := ceilPercent(dfm.IUsed, dfm.Inodes)
		dfm.IUse = percentIUsed / 100.0
		dfms = append(dfms, dfm)
	}
	return dfms, nil
}

// Return true if filesystem should not be taken into account
func excludedFSFromList(fs string, excludedFS []string) bool {
	for _, v := range excludedFS {
		if fs == v {
			return true
		}
	}
	return false
}

// Ceiling function preventing addition of math library
func ceilPercent(v uint64, t uint64) float64 {
	// Prevent division by 0 to occur
	if t == 0 {
		return 0.0
	}
	var v1i uint64
	v1i = v * 100 / t
	var v1f float64
	v1f = float64(v) * 100.0 / float64(t)
	var v2f float64
	v2f = float64(v1i)
	if v2f-1 < v1f && v1f <= v2f+1 {
		addF := 0.0
		if v2f < v1f {
			addF = 1.0
		}
		v1f = v2f + addF
	}
	return v1f
}

func makeNamespace(dfm dfMetric, kind string) []string {
	ns := []string{}
	ns = append(ns, namespacePrefix...)
	ns = append(ns, dfm.MountPoint, kind)
	return ns
}

// validate if metric should be exposed
func validateMetric(namespace []string, dfm dfMetric) bool {
	mountPoint := namespace[0]
	if mountPoint == dfm.MountPoint {
		return true
	}
	return false
}
