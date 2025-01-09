package main

import (
	"context"
	"fmt"
	"github.com/argoproj/argo-cd/pkg/ratelimiter"
	"github.com/argoproj/argo-cd/util/env"
	"math"
	"net/http"
	_ "net/http/pprof"
	"os"
	"time"

	"github.com/argoproj/gitops-engine/pkg/utils/errors"
	"github.com/argoproj/gitops-engine/pkg/utils/kube"
	"github.com/argoproj/pkg/stats"
	"github.com/go-redis/redis"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	// load the gcp plugin (required to authenticate against GKE clusters).
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	// load the oidc plugin (required to authenticate with OpenID Connect).
	_ "k8s.io/client-go/plugin/pkg/client/auth/oidc"
	// load the azure plugin (required to authenticate with AKS clusters).
	_ "k8s.io/client-go/plugin/pkg/client/auth/azure"

	"github.com/argoproj/argo-cd/common"
	"github.com/argoproj/argo-cd/controller"
	"github.com/argoproj/argo-cd/pkg/apis/application/v1alpha1"
	appclientset "github.com/argoproj/argo-cd/pkg/client/clientset/versioned"
	"github.com/argoproj/argo-cd/reposerver/apiclient"
	cacheutil "github.com/argoproj/argo-cd/util/cache"
	appstatecache "github.com/argoproj/argo-cd/util/cache/appstate"
	"github.com/argoproj/argo-cd/util/cli"
	"github.com/argoproj/argo-cd/util/settings"
)

const (
	// CLIName is the name of the CLI
	cliName = "argocd-application-controller"
	// Default time in seconds for application resync period
	defaultAppResyncPeriod = 180
)

func newCommand() *cobra.Command {
	var (
		workqueueRateLimit       ratelimiter.AppControllerRateLimiterConfig
		clientConfig             clientcmd.ClientConfig
		appResyncPeriod          int64
		repoServerAddress        string
		repoServerTimeoutSeconds int
		selfHealTimeoutSeconds   int
		statusProcessors         int
		operationProcessors      int
		logFormat                string
		logLevel                 string
		glogLevel                int
		metricsPort              int
		kubectlParallelismLimit  int64
		cacheSrc                 func() (*appstatecache.Cache, error)
		redisClient              *redis.Client
	)
	var command = cobra.Command{
		Use:   cliName,
		Short: "application-controller is a controller to operate on applications CRD",
		RunE: func(c *cobra.Command, args []string) error {
			cli.SetLogFormat(logFormat)
			cli.SetLogLevel(logLevel)
			cli.SetGLogLevel(glogLevel)

			config, err := clientConfig.ClientConfig()
			errors.CheckError(err)
			errors.CheckError(v1alpha1.SetK8SConfigDefaults(config))

			kubeClient := kubernetes.NewForConfigOrDie(config)
			appClient := appclientset.NewForConfigOrDie(config)

			namespace, _, err := clientConfig.Namespace()
			errors.CheckError(err)

			resyncDuration := time.Duration(appResyncPeriod) * time.Second
			repoClientset := apiclient.NewRepoServerClientset(repoServerAddress, repoServerTimeoutSeconds)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			cache, err := cacheSrc()
			errors.CheckError(err)

			settingsMgr := settings.NewSettingsManager(ctx, kubeClient, namespace)
			kubectl := &kube.KubectlCmd{}
			appController, err := controller.NewApplicationController(
				namespace,
				settingsMgr,
				kubeClient,
				appClient,
				repoClientset,
				cache,
				kubectl,
				resyncDuration,
				time.Duration(selfHealTimeoutSeconds)*time.Second,
				metricsPort,
				kubectlParallelismLimit,
				&workqueueRateLimit)
			errors.CheckError(err)
			cacheutil.CollectMetrics(redisClient, appController.GetMetricsServer())

			vers := common.GetVersion()
			log.Infof("Application Controller (version: %s, built: %s) starting (namespace: %s)", vers.Version, vers.BuildDate, namespace)
			stats.RegisterStackDumper()
			stats.StartStatsTicker(10 * time.Minute)
			stats.RegisterHeapDumper("memprofile")

			go appController.Run(ctx, statusProcessors, operationProcessors)
			go http.ListenAndServe("0.0.0.0:6060", nil)

			// Wait forever
			select {}
		},
	}

	clientConfig = cli.AddKubectlFlagsToCmd(&command)
	command.Flags().Int64Var(&appResyncPeriod, "app-resync", defaultAppResyncPeriod, "Time period in seconds for application resync.")
	command.Flags().StringVar(&repoServerAddress, "repo-server", common.DefaultRepoServerAddr, "Repo server address.")
	command.Flags().IntVar(&repoServerTimeoutSeconds, "repo-server-timeout-seconds", 60, "Repo server RPC call timeout seconds.")
	command.Flags().IntVar(&statusProcessors, "status-processors", 1, "Number of application status processors")
	command.Flags().IntVar(&operationProcessors, "operation-processors", 1, "Number of application operation processors")
	command.Flags().StringVar(&logFormat, "logformat", "text", "Set the logging format. One of: text|json")
	command.Flags().StringVar(&logLevel, "loglevel", "info", "Set the logging level. One of: debug|info|warn|error")
	command.Flags().IntVar(&glogLevel, "gloglevel", 0, "Set the glog logging level")
	command.Flags().IntVar(&metricsPort, "metrics-port", common.DefaultPortArgoCDMetrics, "Start metrics server on given port")
	command.Flags().IntVar(&selfHealTimeoutSeconds, "self-heal-timeout-seconds", 5, "Specifies timeout between application self heal attempts")
	command.Flags().Int64Var(&kubectlParallelismLimit, "kubectl-parallelism-limit", 20, "Number of allowed concurrent kubectl fork/execs. Any value less the 1 means no limit.")
	// global queue rate limit config
	command.Flags().Int64Var(&workqueueRateLimit.BucketSize, "wq-bucket-size", env.ParseInt64FromEnv("WORKQUEUE_BUCKET_SIZE", 500, 1, math.MaxInt64), "Set Workqueue Rate Limiter Bucket Size, default 500")
	command.Flags().Float64Var(&workqueueRateLimit.BucketQPS, "wq-bucket-qps", env.ParseFloat64FromEnv("WORKQUEUE_BUCKET_QPS", math.MaxFloat64, 1, math.MaxFloat64), "Set Workqueue Rate Limiter Bucket QPS, default set to MaxFloat64 which disables the bucket limiter")
	// individual item rate limit config
	// when WORKQUEUE_FAILURE_COOLDOWN is 0 per item rate limiting is disabled(default)
	command.Flags().DurationVar(&workqueueRateLimit.FailureCoolDown, "wq-cooldown-ns", time.Duration(env.ParseInt64FromEnv("WORKQUEUE_FAILURE_COOLDOWN_NS", 0, 0, (24*time.Hour).Nanoseconds())), "Set Workqueue Per Item Rate Limiter Cooldown duration in ns, default 0(per item rate limiter disabled)")
	command.Flags().DurationVar(&workqueueRateLimit.BaseDelay, "wq-basedelay-ns", time.Duration(env.ParseInt64FromEnv("WORKQUEUE_BASE_DELAY_NS", time.Millisecond.Nanoseconds(), time.Nanosecond.Nanoseconds(), (24*time.Hour).Nanoseconds())), "Set Workqueue Per Item Rate Limiter Base Delay duration in nanoseconds, default 1000000 (1ms)")
	command.Flags().DurationVar(&workqueueRateLimit.MaxDelay, "wq-maxdelay-ns", time.Duration(env.ParseInt64FromEnv("WORKQUEUE_MAX_DELAY_NS", time.Second.Nanoseconds(), 1*time.Millisecond.Nanoseconds(), (24*time.Hour).Nanoseconds())), "Set Workqueue Per Item Rate Limiter Max Delay duration in nanoseconds, default 1000000000 (1s)")
	command.Flags().Float64Var(&workqueueRateLimit.BackoffFactor, "wq-backoff-factor", env.ParseFloat64FromEnv("WORKQUEUE_BACKOFF_FACTOR", 1.5, 0, math.MaxFloat64), "Set Workqueue Per Item Rate Limiter Backoff Factor, default is 1.5")
	cacheSrc = appstatecache.AddCacheFlagsToCmd(&command, func(client *redis.Client) {
		redisClient = client
	})
	return &command
}

func main() {
	if err := newCommand().Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
