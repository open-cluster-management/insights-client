package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/golang/glog"
	"github.com/gorilla/mux"
	"github.com/open-cluster-management/insights-client/pkg/config"
	"github.com/open-cluster-management/insights-client/pkg/handlers"
	"github.com/open-cluster-management/insights-client/pkg/monitor"
	"github.com/open-cluster-management/insights-client/pkg/processor"
	"github.com/open-cluster-management/insights-client/pkg/retriever"
	"github.com/open-cluster-management/insights-client/pkg/types"
)

func main() {
	flag.Parse()
	err := flag.Lookup("logtostderr").Value.Set("true")
	if err != nil {
		fmt.Println("Error setting default flag:", err)
		os.Exit(1)
	}
	defer glog.Flush()

	glog.Info("Starting insights-client")
	if commit, ok := os.LookupEnv("VCS_REF"); ok {
		glog.Info("Built from git commit: ", commit)
	}

	fetchClusterIDs := make(chan types.ManagedClusterInfo)
	fetchPolicyReports := make(chan types.ProcessorData)

	monitor := monitor.NewClusterMonitor()
	go monitor.WatchClusters()

	// Set up Retiever and cache the Insights content data
	ret := retriever.NewRetriever(config.Cfg.CCXServer+"/clusters/reports",
		config.Cfg.CCXServer+"/content", nil, "")
	//Wait for hub cluster id to make GET API call
	hubID := "-1"
	for hubID == "-1" {
		hubID = monitor.GetLocalCluster()
		glog.Info("Waiting for local-cluster Id.")
		time.Sleep(2 * time.Second)
	}

	// Wait until we can create the contents map , which will be used to lookup report details
	contents := ret.InitializeContents(hubID)
	retryCount := 1
	for contents < 0 {
		glog.Info("Contents Map not ready. Retrying.")
		time.Sleep(time.Duration(min(300, retryCount*2)) * time.Second)
		contents = ret.InitializeContents(hubID)
		retryCount++
	}

	// Fetch the reports for each cluster & create the PolicyReport resources for each violation.
	go ret.RetrieveCCXReport(hubID, fetchClusterIDs, fetchPolicyReports)

	processor := processor.NewProcessor()
	go processor.CreateUpdatePolicyReports(fetchPolicyReports, ret, hubID)
	//start triggering reports for clusters
	go ret.FetchClusters(monitor, fetchClusterIDs, true)

	router := mux.NewRouter()

	router.HandleFunc("/liveness", handlers.LivenessProbe).Methods("GET")
	router.HandleFunc("/readiness", handlers.ReadinessProbe).Methods("GET")

	// Configure TLS
	cfg := &tls.Config{
		MinVersion:               tls.VersionTLS12,
		CurvePreferences:         []tls.CurveID{tls.CurveP521, tls.CurveP384, tls.CurveP256},
		PreferServerCipherSuites: true,
		CipherSuites: []uint16{
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
		},
	}
	srv := &http.Server{
		Addr:              config.Cfg.ServicePort,
		Handler:           router,
		TLSConfig:         cfg,
		ReadHeaderTimeout: time.Duration(config.Cfg.HTTPTimeout) * time.Millisecond,
		ReadTimeout:       time.Duration(config.Cfg.HTTPTimeout) * time.Millisecond,
		WriteTimeout:      time.Duration(config.Cfg.HTTPTimeout) * time.Millisecond,
		TLSNextProto:      make(map[string]func(*http.Server, *tls.Conn, http.Handler)),
	}

	glog.Info("insights-client listening on", config.Cfg.ServicePort)
	log.Fatal(srv.ListenAndServeTLS("./sslcert/tls.crt", "./sslcert/tls.key"),
		" Use ./setup.sh to generate certificates for local development.")
}

// Returns the smaller of two ints
func min(a, b int) int {
	if a > b {
		return b
	}
	return a
}
