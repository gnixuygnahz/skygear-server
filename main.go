package main

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"

	log "github.com/Sirupsen/logrus"
	"github.com/robfig/cron"

	"github.com/oursky/ourd/asset"
	"github.com/oursky/ourd/authtoken"
	"github.com/oursky/ourd/handler"
	"github.com/oursky/ourd/hook"
	"github.com/oursky/ourd/oddb"
	_ "github.com/oursky/ourd/oddb/fs"
	_ "github.com/oursky/ourd/oddb/pq"
	"github.com/oursky/ourd/plugin"
	_ "github.com/oursky/ourd/plugin/exec"
	"github.com/oursky/ourd/push"
	"github.com/oursky/ourd/router"
	"github.com/oursky/ourd/subscription"
)

type responseLogger struct {
	w      http.ResponseWriter
	status int
	size   int
	b      bytes.Buffer
}

func (l *responseLogger) Header() http.Header {
	return l.w.Header()
}

func (l *responseLogger) Write(b []byte) (int, error) {
	if l.status == 0 {
		// The status will be StatusOK if WriteHeader has not been called yet
		l.status = http.StatusOK
	}
	l.b.Write(b)
	size, err := l.w.Write(b)
	l.size += size
	return size, err
}

func (l *responseLogger) WriteHeader(s int) {
	l.w.WriteHeader(s)
	l.status = s
}

func (l *responseLogger) Status() int {
	return l.status
}

func (l *responseLogger) Size() int {
	return l.size
}

func (l *responseLogger) String() string {
	return l.b.String()
}

func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Debugf("%v %v", r.Method, r.RequestURI)

		log.Debugln("------ Header: ------")
		for key, value := range r.Header {
			log.Debugf("%s: %v", key, value)
		}

		body, _ := ioutil.ReadAll(r.Body)
		r.Body = ioutil.NopCloser(bytes.NewReader(body))

		log.Debugln("------ Request: ------")
		if r.Header.Get("Content-Type") == "" || r.Header.Get("Content-Type") == "application/json" {
			log.Debugln(string(body))
		} else {
			log.Debugf("%d bytes of body", len(body))
		}

		rlogger := &responseLogger{w: w}
		next.ServeHTTP(rlogger, r)

		log.Debugln("------ Response: ------")
		if w.Header().Get("Content-Type") == "" || w.Header().Get("Content-Type") == "application/json" {
			log.Debugln(rlogger.String())
		} else {
			log.Debugf("%d bytes of body", len(rlogger.String()))
		}
	})
}

func usage() {
	fmt.Println("Usage: ourd [<config file>]")
}

func main() {
	var configPath string
	if len(os.Args) < 2 {
		configPath = os.Getenv("OD_CONFIG")
		if configPath == "" {
			usage()
			return
		}
	} else {
		configPath = os.Args[1]
	}

	config := Configuration{}
	if err := ReadFileInto(&config, configPath); err != nil {
		fmt.Println(err.Error())
		return
	}

	if config.Subscription.Enabled {
		var gateway string
		switch config.APNS.Env {
		case "sandbox":
			gateway = "gateway.sandbox.push.apple.com:2195"
		case "production":
			gateway = "gateway.push.apple.com:2195"
		default:
			fmt.Println("config: apns.env can only be sandbox or production")
			return
		}

		pushSender, err := push.NewAPNSPusher(gateway, config.APNS.Cert, config.APNS.Key)
		if err != nil {
			log.Fatalf("Failed to set up push sender: %v", err)
		}

		if err := pushSender.Init(); err != nil {
			log.Fatalf("Failed to init push sender: %v", err)
		}

		subscriptionService := &subscription.Service{
			ConnOpener:         func() (oddb.Conn, error) { return oddb.Open(config.DB.ImplName, config.App.Name, config.DB.Option) },
			NotificationSender: pushSender,
		}
		go subscriptionService.Init().Listen()
		log.Infoln("Subscription Service listening...")
	}

	// Setup Logging
	log.SetOutput(os.Stderr)
	logLv, logE := log.ParseLevel(config.LOG.Level)
	if logE != nil {
		logLv = log.DebugLevel
	}
	log.SetLevel(logLv)

	naiveAPIKeyPreprocessor := apiKeyValidatonPreprocessor{
		Key:     config.App.APIKey,
		AppName: config.App.Name,
	}

	fileTokenStorePreprocessor := tokenStorePreprocessor{
		Store: authtoken.FileStore(config.TokenStore.Path).Init(),
	}

	var store asset.Store
	switch config.AssetStore.ImplName {
	default:
		panic("unrecgonized asset store implementation: " + config.AssetStore.ImplName)
	case "fs":
		store = asset.NewFileStore(
			config.AssetStore.Path,
			config.AssetURLSigner.URLPrefix,
			config.AssetURLSigner.Secret)
	case "s3":
		s3Store, err := asset.NewS3Store(
			config.AssetStore.AccessToken,
			config.AssetStore.SecretToken,
			config.AssetStore.Reigon,
			config.AssetStore.Bucket,
		)
		if err != nil {
			panic("failed to initialize asset.S3Store: " + err.Error())
		}
		store = s3Store
	}
	assetStorePreprocessor := assetStorePreprocessor{
		Store: store,
	}

	authenticator := userAuthenticator{
		APIKey:  config.App.APIKey,
		AppName: config.App.Name,
	}

	fileSystemConnPreprocessor := connPreprocessor{
		AppName:  config.App.Name,
		DBOpener: oddb.Open,
		DBImpl:   config.DB.ImplName,
		Option:   config.DB.Option,
	}

	r := router.NewRouter()
	r.Map("", handler.HomeHandler)

	authPreprocessors := []router.Processor{
		naiveAPIKeyPreprocessor.Preprocess,
		fileSystemConnPreprocessor.Preprocess,
		fileTokenStorePreprocessor.Preprocess,
	}
	r.Map("auth:signup", handler.SignupHandler, authPreprocessors...)
	r.Map("auth:login", handler.LoginHandler, authPreprocessors...)
	r.Map("auth:logout", handler.LogoutHandler,
		fileTokenStorePreprocessor.Preprocess,
		authenticator.Preprocess,
	)

	hookRegistry := hook.NewRegistry()
	hookRegistryPreprocessor := hookRegistryPreprocessor{
		Registry: hookRegistry,
	}

	recordReadPreprocessors := []router.Processor{
		fileTokenStorePreprocessor.Preprocess,
		authenticator.Preprocess,
		fileSystemConnPreprocessor.Preprocess,
		assetStorePreprocessor.Preprocess,
		injectUserIfPresent,
		injectDatabase,
	}
	recordWritePreprocessors := []router.Processor{
		hookRegistryPreprocessor.Preprocess,
		fileTokenStorePreprocessor.Preprocess,
		authenticator.Preprocess,
		fileSystemConnPreprocessor.Preprocess,
		assetStorePreprocessor.Preprocess,
		injectUserIfPresent,
		injectDatabase,
		requireUserForWrite,
	}
	r.Map("record:fetch", handler.RecordFetchHandler, recordReadPreprocessors...)
	r.Map("record:query", handler.RecordQueryHandler, recordReadPreprocessors...)
	r.Map("record:save", handler.RecordSaveHandler, recordWritePreprocessors...)
	r.Map("record:delete", handler.RecordDeleteHandler, recordWritePreprocessors...)

	assetGetPreprocessors := []router.Processor{
		fileSystemConnPreprocessor.Preprocess,
		assetStorePreprocessor.Preprocess,
	}
	assetUploadPreprocessors := []router.Processor{
		fileTokenStorePreprocessor.Preprocess,
		authenticator.Preprocess,
		fileSystemConnPreprocessor.Preprocess,
		assetStorePreprocessor.Preprocess,
	}
	r.GET(`files/(.+)`, handler.AssetGetURLHandler, assetGetPreprocessors...)
	r.PUT(`files/(.+)`, handler.AssetUploadURLHandler, assetUploadPreprocessors...)

	r.Map("device:register",
		handler.DeviceRegisterHandler,
		fileTokenStorePreprocessor.Preprocess,
		authenticator.Preprocess,
		fileSystemConnPreprocessor.Preprocess,
		injectUserIfPresent,
	)

	// subscription shares the same set of preprocessor as record read at the moment
	r.Map("subscription:fetch_all", handler.SubscriptionFetchAllHandler, recordReadPreprocessors...)
	r.Map("subscription:fetch", handler.SubscriptionFetchHandler, recordReadPreprocessors...)
	r.Map("subscription:save", handler.SubscriptionSaveHandler, recordReadPreprocessors...)
	r.Map("subscription:delete", handler.SubscriptionDeleteHandler, recordReadPreprocessors...)

	// relation shares the same setof preprocessor
	r.Map("relation:query", handler.RelationQueryHandler, recordReadPreprocessors...)
	r.Map("relation:add", handler.RelationAddHandler, recordReadPreprocessors...)
	r.Map("relation:remove", handler.RelationRemoveHandler, recordReadPreprocessors...)

	userReadPreprocessors := []router.Processor{
		fileTokenStorePreprocessor.Preprocess,
		authenticator.Preprocess,
		fileSystemConnPreprocessor.Preprocess,
		injectUserIfPresent,
		injectDatabase,
	}
	userWritePreprocessors := []router.Processor{
		fileTokenStorePreprocessor.Preprocess,
		authenticator.Preprocess,
		fileSystemConnPreprocessor.Preprocess,
		injectUserIfPresent,
		injectDatabase,
		requireUserForWrite,
	}
	r.Map("user:query", handler.UserQueryHandler, userReadPreprocessors...)
	r.Map("user:update", handler.UserUpdateHandler, userWritePreprocessors...)

	plugins := []plugin.Plugin{}
	for _, pluginConfig := range config.Plugin {
		p := plugin.NewPlugin(pluginConfig.Transport, pluginConfig.Path, pluginConfig.Args)

		plugins = append(plugins, p)
	}

	c := cron.New()
	for _, plug := range plugins {
		plug.Init(r, hookRegistry, c)
	}
	c.Start()

	log.Printf("Listening on %v...", config.HTTP.Host)
	err := http.ListenAndServe(config.HTTP.Host, logMiddleware(r))
	if err != nil {
		log.Printf("Failed: %v", err)
	}
}
