package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

//go:embed frontend/*
var frontendFiles embed.FS

type Config struct {
	HTTPAddr             string
	PrometheusURL        string
	LLMURL               string
	LLMModel             string
	Namespace            string
	ManagedLabelKey      string
	ManagedLabelValue    string
	AllowedDeployment    string
	RecoveryConfigMap    string
	PollInterval         time.Duration
	VerifyTimeout        time.Duration
	MinimumConfidence    float64
	MaximumLogCharacters int
}

type PrometheusAPIResponse struct {
	Status string `json:"status"`
	Data   struct {
		Alerts []PrometheusAlert `json:"alerts"`
	} `json:"data"`
}

type PrometheusAlert struct {
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	State       string            `json:"state"`
	ActiveAt    time.Time         `json:"activeAt"`
}

type Incident struct {
	AlertName      string            `json:"alertName"`
	Namespace      string            `json:"namespace"`
	PodName        string            `json:"podName"`
	DeploymentName string            `json:"deploymentName"`
	Reason         string            `json:"reason"`
	PodPhase       string            `json:"podPhase"`
	RestartCount   int32             `json:"restartCount"`
	Labels         map[string]string `json:"labels"`
	PreviousLogs   string            `json:"previousLogs"`
	LastEvent      string            `json:"lastEvent"`
	ActiveAt       time.Time         `json:"activeAt"`
}

type Decision struct {
	Action     string  `json:"action"`
	Reason     string  `json:"reason"`
	Confidence float64 `json:"confidence"`
}

type ActionResult struct {
	Action      string    `json:"action"`
	Target      string    `json:"target"`
	Success     bool      `json:"success"`
	Message     string    `json:"message"`
	StartedAt   time.Time `json:"startedAt"`
	CompletedAt time.Time `json:"completedAt"`
}

type TimelineEvent struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}

type DashboardStatus struct {
	AgentStatus         string          `json:"agentStatus"`
	LastCheck           time.Time       `json:"lastCheck"`
	PrometheusReachable bool            `json:"prometheusReachable"`
	LLMReachable        bool            `json:"llmReachable"`
	LastIncident        *Incident       `json:"lastIncident,omitempty"`
	LastDecision        *Decision       `json:"lastDecision,omitempty"`
	LastAction          *ActionResult   `json:"lastAction,omitempty"`
	Timeline            []TimelineEvent `json:"timeline"`
}

type StateStore struct {
	mu     sync.RWMutex
	status DashboardStatus
}

func NewStateStore() *StateStore {
	return &StateStore{status: DashboardStatus{
		AgentStatus: "starting",
		Timeline:    make([]TimelineEvent, 0, 20),
	}}
}

func (s *StateStore) Snapshot() DashboardStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	copyStatus := s.status
	copyStatus.Timeline = append([]TimelineEvent(nil), s.status.Timeline...)
	return copyStatus
}

func (s *StateStore) Update(fn func(*DashboardStatus)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.status)
}

func (s *StateStore) AddEvent(level, message string) {
	s.Update(func(status *DashboardStatus) {
		status.Timeline = append([]TimelineEvent{{
			Time:    time.Now(),
			Level:   level,
			Message: message,
		}}, status.Timeline...)
		if len(status.Timeline) > 20 {
			status.Timeline = status.Timeline[:20]
		}
	})
}

type OpenAIChatRequest struct {
	Model       string          `json:"model"`
	Messages    []OpenAIMessage `json:"messages"`
	Temperature float64         `json:"temperature"`
	MaxTokens   int             `json:"max_tokens"`
}

type OpenAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type OpenAIChatResponse struct {
	Choices []struct {
		Message OpenAIMessage `json:"message"`
	} `json:"choices"`
}

var httpClient = &http.Client{Timeout: 70 * time.Second}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("invalid configuration: %v", err)
	}
	clientset, err := getKubernetesClient()
	if err != nil {
		log.Fatalf("create kubernetes client: %v", err)
	}

	store := NewStateStore()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runAgentLoop(ctx, clientset, cfg, store)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if err := json.NewEncoder(w).Encode(store.Snapshot()); err != nil {
			log.Printf("encode status response: %v", err)
		}
	})

	frontendRoot, err := fs.Sub(frontendFiles, "frontend")
	if err != nil {
		log.Fatalf("load embedded frontend: %v", err)
	}
	mux.Handle("/", http.FileServer(http.FS(frontendRoot)))

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           requestLogger(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("SRE Agent dashboard listening on %s", cfg.HTTPAddr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("http server stopped: %v", err)
	}
}

func loadConfig() (Config, error) {
	pollSeconds, err := envInt("POLL_INTERVAL_SECONDS", 15)
	if err != nil {
		return Config{}, err
	}
	verifySeconds, err := envInt("VERIFY_TIMEOUT_SECONDS", 120)
	if err != nil {
		return Config{}, err
	}
	minimumConfidence, err := envFloat("MINIMUM_CONFIDENCE", 0.60)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		HTTPAddr:             envOrDefault("HTTP_ADDR", ":8080"),
		PrometheusURL:        strings.TrimRight(envOrDefault("PROMETHEUS_URL", "http://prometheus-stack-kube-prom-prometheus.monitoring.svc.cluster.local:9090"), "/"),
		LLMURL:               strings.TrimRight(envOrDefault("LLM_URL", "http://192.168.30.100:13533"), "/"),
		LLMModel:             envOrDefault("LLM_MODEL", "qwen2.5-3b-instruct-q4_k_m.gguf"),
		Namespace:            envOrDefault("WATCH_NAMESPACE", "sre-demo"),
		ManagedLabelKey:      envOrDefault("MANAGED_LABEL_KEY", "sre.ai/managed"),
		ManagedLabelValue:    envOrDefault("MANAGED_LABEL_VALUE", "true"),
		AllowedDeployment:    envOrDefault("ALLOWED_DEPLOYMENT", "faulty-app"),
		RecoveryConfigMap:    envOrDefault("RECOVERY_CONFIGMAP", "fault-switch"),
		PollInterval:         time.Duration(pollSeconds) * time.Second,
		VerifyTimeout:        time.Duration(verifySeconds) * time.Second,
		MinimumConfidence:    minimumConfidence,
		MaximumLogCharacters: 4000,
	}
	if cfg.PollInterval < 5*time.Second {
		return Config{}, fmt.Errorf("POLL_INTERVAL_SECONDS must be at least 5")
	}
	if cfg.MinimumConfidence < 0 || cfg.MinimumConfidence > 1 {
		return Config{}, fmt.Errorf("MINIMUM_CONFIDENCE must be between 0 and 1")
	}
	return cfg, nil
}

func runAgentLoop(ctx context.Context, clientset *kubernetes.Clientset, cfg Config, store *StateStore) {
	store.AddEvent("info", "Agent loop started")
	runOneCycle(ctx, clientset, cfg, store)
	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runOneCycle(ctx, clientset, cfg, store)
		}
	}
}

func runOneCycle(ctx context.Context, clientset *kubernetes.Clientset, cfg Config, store *StateStore) {
	store.Update(func(status *DashboardStatus) {
		status.AgentStatus = "sensing"
		status.LastCheck = time.Now()
	})

	alerts, err := sense(ctx, clientset, cfg)
	if err != nil {
		store.Update(func(status *DashboardStatus) {
			status.AgentStatus = "degraded"
			status.PrometheusReachable = false
		})
		store.AddEvent("error", "感知失败: "+err.Error())
		return
	}
	store.Update(func(status *DashboardStatus) {
		status.PrometheusReachable = true
	})

	if len(alerts) == 0 {
		llmOK := checkLLMReachable(ctx, cfg)
		store.Update(func(status *DashboardStatus) {
			status.AgentStatus = "healthy"
			status.LLMReachable = llmOK
		})
		return
	}

	incident := alerts[0]
	store.Update(func(status *DashboardStatus) {
		status.AgentStatus = "deciding"
		status.LastIncident = &incident
	})
	store.AddEvent("warning", fmt.Sprintf("发现故障 Pod: %s/%s", incident.Namespace, incident.PodName))

	decision, err := decide(ctx, cfg, incident)
	if err != nil {
		store.Update(func(status *DashboardStatus) {
			status.AgentStatus = "degraded"
			status.LLMReachable = false
		})
		store.AddEvent("error", "LLM 决策失败: "+err.Error())
		return
	}
	store.Update(func(status *DashboardStatus) {
		status.LLMReachable = true
		status.LastDecision = &decision
	})
	store.AddEvent("info", fmt.Sprintf("LLM 决策: %s，置信度 %.2f", decision.Action, decision.Confidence))

	if err := validateDecision(cfg, incident, decision); err != nil {
		store.Update(func(status *DashboardStatus) {
			status.AgentStatus = "blocked"
		})
		store.AddEvent("warning", "安全校验阻止执行: "+err.Error())
		return
	}

	store.Update(func(status *DashboardStatus) {
		status.AgentStatus = "acting"
	})
	actionResult := actAndVerify(ctx, clientset, cfg, incident, decision)
	store.Update(func(status *DashboardStatus) {
		status.LastAction = &actionResult
		if actionResult.Success {
			status.AgentStatus = "recovered"
		} else {
			status.AgentStatus = "degraded"
		}
	})
	if actionResult.Success {
		store.AddEvent("success", actionResult.Message)
	} else {
		store.AddEvent("error", actionResult.Message)
	}
}

// sense queries firing Prometheus alerts and enriches them with Kubernetes context.
// Alerts for Pods that have already disappeared are skipped because Prometheus can
// retain an alert briefly after the recovery action has completed.
func sense(ctx context.Context, clientset *kubernetes.Clientset, cfg Config) ([]Incident, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.PrometheusURL+"/api/v1/alerts", nil)
	if err != nil {
		return nil, fmt.Errorf("create Prometheus request: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request Prometheus alerts: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Prometheus returned HTTP %d: %s", resp.StatusCode, string(body[:min(len(body), 200)]))
	}

	var promResp PrometheusAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&promResp); err != nil {
		return nil, fmt.Errorf("decode Prometheus response: %w", err)
	}

	if promResp.Status != "success" {
		return nil, fmt.Errorf("Prometheus API status: %s", promResp.Status)
	}

	var incidents []Incident
	for _, alert := range promResp.Data.Alerts {
		if alert.State != "firing" {
			continue
		}
		if alert.Labels["alertname"] != "PodCrashLooping" {
			continue
		}

		namespace := alert.Labels["namespace"]
		podName := alert.Labels["pod"]
		if namespace == "" || podName == "" {
			continue
		}

		incident, err := enrichIncident(ctx, clientset, alert, namespace, podName, cfg.MaximumLogCharacters)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				continue
			}
			return nil, fmt.Errorf("enrich incident %s/%s: %w", namespace, podName, err)
		}
		incidents = append(incidents, incident)
	}

	return incidents, nil
}

func enrichIncident(ctx context.Context, clientset *kubernetes.Clientset, alert PrometheusAlert, namespace, podName string, maxLogChars int) (Incident, error) {
	pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return Incident{}, fmt.Errorf("get pod %s/%s: %w", namespace, podName, err)
	}
	deploymentName, err := resolveDeploymentOwner(ctx, clientset, pod)
	if err != nil {
		return Incident{}, err
	}

	var restartCount int32
	for _, status := range pod.Status.ContainerStatuses {
		restartCount += status.RestartCount
	}

	return Incident{
		AlertName:      alert.Labels["alertname"],
		Namespace:      namespace,
		PodName:        podName,
		DeploymentName: deploymentName,
		Reason:         alert.Annotations["description"],
		PodPhase:       string(pod.Status.Phase),
		RestartCount:   restartCount,
		Labels:         pod.Labels,
		PreviousLogs:   readPodLogs(ctx, clientset, namespace, podName, maxLogChars),
		LastEvent:      readLatestPodEvent(ctx, clientset, pod),
		ActiveAt:       alert.ActiveAt,
	}, nil
}

func resolveDeploymentOwner(ctx context.Context, clientset *kubernetes.Clientset, pod *corev1.Pod) (string, error) {
	for _, owner := range pod.OwnerReferences {
		if owner.Kind != "ReplicaSet" {
			continue
		}
		rs, err := clientset.AppsV1().ReplicaSets(pod.Namespace).Get(ctx, owner.Name, metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("get ReplicaSet %s/%s: %w", pod.Namespace, owner.Name, err)
		}
		for _, rsOwner := range rs.OwnerReferences {
			if rsOwner.Kind == "Deployment" {
				return rsOwner.Name, nil
			}
		}
	}
	return "", fmt.Errorf("pod %s/%s is not owned by a Deployment", pod.Namespace, pod.Name)
}

func readPodLogs(ctx context.Context, clientset *kubernetes.Clientset, namespace, podName string, maxChars int) string {
	tailLines := int64(80)
	options := &corev1.PodLogOptions{Previous: true, TailLines: &tailLines}
	data, err := clientset.CoreV1().Pods(namespace).GetLogs(podName, options).DoRaw(ctx)
	if err != nil {
		options.Previous = false
		data, err = clientset.CoreV1().Pods(namespace).GetLogs(podName, options).DoRaw(ctx)
	}
	if err != nil {
		return "日志读取失败: " + err.Error()
	}
	text := strings.TrimSpace(string(data))
	if len(text) > maxChars {
		text = text[len(text)-maxChars:]
	}
	return text
}

func readLatestPodEvent(ctx context.Context, clientset *kubernetes.Clientset, pod *corev1.Pod) string {
	selector := fields.OneTermEqualSelector("involvedObject.uid", string(pod.UID)).String()
	events, err := clientset.CoreV1().Events(pod.Namespace).List(ctx, metav1.ListOptions{FieldSelector: selector})
	if err != nil || len(events.Items) == 0 {
		return ""
	}
	latest := events.Items[0]
	for _, event := range events.Items[1:] {
		if event.LastTimestamp.After(latest.LastTimestamp.Time) {
			latest = event
		}
	}
	return strings.TrimSpace(latest.Reason + ": " + latest.Message)
}

func decide(ctx context.Context, cfg Config, incident Incident) (Decision, error) {
	systemPrompt := "你是一个 SRE 运维助手。根据故障信息判断是否需要重启 Deployment。"
	systemPrompt += "只返回一个 JSON 对象，不要附带任何其他文字。JSON 格式如下："
	systemPrompt += `{"action":"restart_deployment 或 ignore","reason":"简短中文理由","confidence":0.0~1.0}`

	userPrompt := fmt.Sprintf("故障信息：alert=%s namespace=%s pod=%s deployment=%s reason=%s phase=%s restartCount=%d lastEvent=%s logs=%s",
		incident.AlertName, incident.Namespace, incident.PodName, incident.DeploymentName,
		incident.Reason, incident.PodPhase, incident.RestartCount,
		incident.LastEvent, incident.PreviousLogs)

	chatReq := OpenAIChatRequest{
		Model:       cfg.LLMModel,
		Temperature: 0.1,
		MaxTokens:   256,
		Messages: []OpenAIMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
	}

	body, err := json.Marshal(chatReq)
	if err != nil {
		return Decision{}, fmt.Errorf("marshal chat request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.LLMURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Decision{}, fmt.Errorf("create LLM request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return Decision{}, fmt.Errorf("request LLM: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return Decision{}, fmt.Errorf("LLM returned HTTP %d: %s", resp.StatusCode, string(respBody[:min(len(respBody), 200)]))
	}

	var chatResp OpenAIChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return Decision{}, fmt.Errorf("decode LLM response: %w", err)
	}

	if len(chatResp.Choices) == 0 {
		return Decision{}, errors.New("LLM returned no choices")
	}

	jsonText, err := extractJSONObject(chatResp.Choices[0].Message.Content)
	if err != nil {
		return Decision{}, err
	}

	var decision Decision
	if err := json.Unmarshal([]byte(jsonText), &decision); err != nil {
		return Decision{}, fmt.Errorf("parse decision JSON: %w", err)
	}

	if decision.Action != "restart_deployment" && decision.Action != "ignore" {
		return Decision{}, fmt.Errorf("LLM returned invalid action: %s", decision.Action)
	}
	if decision.Confidence < 0 || decision.Confidence > 1 {
		return Decision{}, fmt.Errorf("LLM returned invalid confidence: %.2f", decision.Confidence)
	}

	return decision, nil
}

func extractJSONObject(text string) (string, error) {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end < start {
		return "", fmt.Errorf("LLM did not return a JSON object: %q", text)
	}
	return text[start : end+1], nil
}

func validateDecision(cfg Config, incident Incident, decision Decision) error {
	// 校验1：动作必须是 restart_deployment 或 ignore
	if decision.Action != "restart_deployment" && decision.Action != "ignore" {
		return fmt.Errorf("未知的动作: %s", decision.Action)
	}

	// ignore 动作不需要后续校验
	if decision.Action == "ignore" {
		return nil
	}

	// 校验2：置信度必须达到最低阈值
	if decision.Confidence < cfg.MinimumConfidence {
		return fmt.Errorf("置信度 %.2f 低于最低阈值 %.2f", decision.Confidence, cfg.MinimumConfidence)
	}

	// 校验3：命名空间必须匹配
	if incident.Namespace != cfg.Namespace {
		return fmt.Errorf("命名空间不匹配: 期望 %s, 实际 %s", cfg.Namespace, incident.Namespace)
	}

	// 校验4：Deployment 必须在允许列表中
	if incident.DeploymentName != cfg.AllowedDeployment {
		return fmt.Errorf("Deployment %s 不在允许列表 (允许: %s)", incident.DeploymentName, cfg.AllowedDeployment)
	}

	// 校验5：Pod 必须带有受管标签
	labelValue, ok := incident.Labels[cfg.ManagedLabelKey]
	if !ok || labelValue != cfg.ManagedLabelValue {
		return fmt.Errorf("Pod 缺少受管标签 %s=%s", cfg.ManagedLabelKey, cfg.ManagedLabelValue)
	}

	return nil
}

func actAndVerify(ctx context.Context, clientset *kubernetes.Clientset, cfg Config, incident Incident, decision Decision) ActionResult {
	result := ActionResult{
		Action:    decision.Action,
		Target:    incident.Namespace + "/" + incident.PodName,
		StartedAt: time.Now(),
	}

	if decision.Action == "ignore" {
		result.Success = true
		result.Message = "LLM 决策为忽略，无需执行恢复操作"
		result.CompletedAt = time.Now()
		return result
	}

	// 步骤1：将 fault-switch 的 FAIL_MODE 设为 false
	cm, err := clientset.CoreV1().ConfigMaps(cfg.Namespace).Get(ctx, cfg.RecoveryConfigMap, metav1.GetOptions{})
	if err != nil {
		result.Success = false
		result.Message = fmt.Sprintf("获取 ConfigMap %s/%s 失败: %v", cfg.Namespace, cfg.RecoveryConfigMap, err)
		result.CompletedAt = time.Now()
		return result
	}

	cm.Data["FAIL_MODE"] = "false"
	_, err = clientset.CoreV1().ConfigMaps(cfg.Namespace).Update(ctx, cm, metav1.UpdateOptions{})
	if err != nil {
		result.Success = false
		result.Message = fmt.Sprintf("更新 ConfigMap %s/%s 失败: %v", cfg.Namespace, cfg.RecoveryConfigMap, err)
		result.CompletedAt = time.Now()
		return result
	}

	// 步骤2：删除故障 Pod
	err = clientset.CoreV1().Pods(incident.Namespace).Delete(ctx, incident.PodName, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		result.Success = false
		result.Message = fmt.Sprintf("删除 Pod %s/%s 失败: %v", incident.Namespace, incident.PodName, err)
		result.CompletedAt = time.Now()
		return result
	}

	// 步骤3：等待 Pod 完全删除（带超时）
	verifyCtx, cancel := context.WithTimeout(ctx, cfg.VerifyTimeout)
	defer cancel()

	if err := waitForPodDeleted(verifyCtx, clientset, incident.Namespace, incident.PodName); err != nil {
		result.Success = false
		result.Message = fmt.Sprintf("Pod %s/%s 在超时内未删除: %v", incident.Namespace, incident.PodName, err)
		result.CompletedAt = time.Now()
		return result
	}

	// 步骤4：等待 Deployment 完全就绪
	if err := waitForDeploymentReady(verifyCtx, clientset, incident.Namespace, incident.DeploymentName); err != nil {
		result.Success = false
		result.Message = fmt.Sprintf("Deployment %s/%s 在超时内未就绪: %v", incident.Namespace, incident.DeploymentName, err)
		result.CompletedAt = time.Now()
		return result
	}

	result.Success = true
	result.Message = fmt.Sprintf("成功恢复 %s/%s: FAIL_MODE 已设为 false, Pod 已删除, Deployment 已就绪",
		incident.Namespace, incident.DeploymentName)
	result.CompletedAt = time.Now()
	return result
}

func waitForPodDeleted(ctx context.Context, clientset *kubernetes.Clientset, namespace, name string) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		_, err := clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func waitForDeploymentReady(ctx context.Context, clientset *kubernetes.Clientset, namespace, name string) error {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		deployment, err := clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err == nil && deploymentReady(deployment) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func deploymentReady(deployment *appsv1.Deployment) bool {
	desired := int32(1)
	if deployment.Spec.Replicas != nil {
		desired = *deployment.Spec.Replicas
	}
	return deployment.Status.ObservedGeneration >= deployment.Generation &&
		deployment.Status.UpdatedReplicas >= desired &&
		deployment.Status.ReadyReplicas >= desired &&
		deployment.Status.AvailableReplicas >= desired &&
		deployment.Status.UnavailableReplicas == 0
}

func checkLLMReachable(ctx context.Context, cfg Config) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.LLMURL+"/v1/models", nil)
	if err != nil {
		return false
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func getKubernetesClient() (*kubernetes.Clientset, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return nil, fmt.Errorf("in-cluster config unavailable and home lookup failed: %w", err)
		}
		config, err = clientcmd.BuildConfigFromFlags("", filepath.Join(home, ".kube", "config"))
		if err != nil {
			return nil, fmt.Errorf("load kubeconfig: %w", err)
		}
	}
	return kubernetes.NewForConfig(config)
}

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("http %s %s %s", r.Method, r.URL.Path, time.Since(started).Round(time.Millisecond))
	})
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	return parsed, nil
}

func envFloat(key string, fallback float64) (float64, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number: %w", key, err)
	}
	return parsed, nil
}
