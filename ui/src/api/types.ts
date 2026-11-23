// Wire types for the parts of the Serendip API the console uses. They
// mirror core/resource (Go) exactly; the console never invents fields.

export type ConditionStatus = "True" | "False" | "Unknown";

export interface Condition {
  type: string;
  status: ConditionStatus;
  reason?: string;
  message?: string;
  lastTransitionTime: string;
}

export interface OwnerRef {
  kind: string;
  id: string;
}

export interface Relationship {
  type: string;
  targetKind: string;
  targetId: string;
}

export interface Metadata {
  id: string;
  name: string;
  kind: string;
  labels?: Record<string, string>;
  ownerRefs?: OwnerRef[];
  relationships?: Relationship[];
  createdAt: string;
  updatedAt: string;
  resourceVersion: number;
}

export interface Resource<Spec, Status> {
  metadata: Metadata;
  spec: Spec;
  status: Status;
}

export interface BaseStatus {
  conditions?: Condition[];
}

export type ApplicationSpec = Record<string, never>;
export type Application = Resource<ApplicationSpec, BaseStatus>;

export interface ComponentPort {
  name: string;
  port: number;
  expose?: boolean;
}

/** One call to another Component's port; env receives its address (spec 020). */
export interface ComponentDependency {
  component: string;
  port: string;
  env: string;
}

/** Where a Component's image is built from (spec 022). */
export interface ComponentSource {
  repository: string;
  /** Defaults to main. */
  branch?: string;
  /** The build context in the repository; defaults to ".". */
  path?: string;
  /** Relative to path; defaults to Dockerfile. */
  dockerfile?: string;
  /** The ID of a Secret holding the repository's credential. */
  credentialSecret?: string;
  autoBuild?: boolean;
}

export interface ComponentSpec {
  /** Empty when the Component is built from source. */
  image: string;
  source?: ComponentSource;
  ports?: ComponentPort[];
  env?: Record<string, string>;
  dependencies?: ComponentDependency[];
}

export interface ComponentEndpoint {
  name: string;
  url: string;
}

export interface ComponentStatus extends BaseStatus {
  endpoints?: ComponentEndpoint[];
  /** The built image a source Component runs, once a build has succeeded. */
  image?: string;
}

export type Component = Resource<ComponentSpec, ComponentStatus>;

export type NodeResource = Resource<Record<string, unknown>, BaseStatus>;

/** GET /v1/readiness: capability name to its readiness. */
export interface CapabilityReadiness {
  ready: boolean;
  reason: string;
  message?: string;
  since: string;
}
export type Readiness = Record<string, CapabilityReadiness>;

/** GET /v1/session */
export interface SessionInfo {
  actor: string;
  method: "session" | "bearer";
  subject?: string;
  expiresAt?: string;
  idleExpiresAt?: string;
}

/** RFC 7807 problem+json, as api/problem.go writes it. */
export interface Problem {
  title: string;
  status: number;
  detail?: string;
  instance?: string;
}

// Authorization (spec 017): Azure-style roles at scopes.

/** "/" (the zero value: no kind, no id) or one resource. */
export interface Scope {
  kind?: string;
  id?: string;
}

export interface Permission {
  actions?: string[];
  notActions?: string[];
  dataActions?: string[];
  notDataActions?: string[];
}

export interface RoleDefinitionSpec {
  roleName: string;
  description?: string;
  permissions: Permission[];
  assignableScopes?: Scope[];
}

/**
 * GET /v1/authorization/roleDefinitions: built-ins are marked, and the
 * server says which roles an agent may hold (spec 018).
 */
export type RoleDefinition = Resource<RoleDefinitionSpec, BaseStatus> & { builtIn: boolean; assignableToAgents?: boolean };

export type PrincipalType = "person" | "group" | "apiKey" | "agent";

export interface PrincipalRef {
  type: PrincipalType;
  id: string;
  /** The person accountable for an agent; required for, and only on, an agent. */
  sponsor?: PrincipalRef;
}

export interface RoleAssignmentSpec {
  principal: PrincipalRef;
  roleDefinitionId: string;
  scope: Scope;
  description?: string;
}

/** Listed at a scope, an assignment made higher up is marked inherited. */
export type RoleAssignment = Resource<RoleAssignmentSpec, BaseStatus> & { inherited?: boolean };

/** GET /v1/authorization/permissions?scope= */
export interface PermissionsView {
  scope: string;
  permissions: Permission[];
}

/** GET /v1/authorization/principals */
export interface KnownPrincipal {
  type: PrincipalType;
  id: string;
  displayName?: string;
  groups?: string[];
  firstSeen: string;
  lastSeen: string;
}

/** Where a directory answer came from: the identity provider now, or sign-ins. */
export type DirectorySource = "live" | "signedIn";

/** One person or group that can be granted a role. */
export interface DirectoryItem {
  type: PrincipalType;
  id: string;
  displayName?: string;
}

/** GET /v1/authorization/directory?type=&q= */
export interface DirectoryView {
  source: DirectorySource;
  truncated: boolean;
  items: DirectoryItem[];
}

/** GET /v1/authorization/principals/person/{id}/groups */
export interface GroupsView {
  source: DirectorySource;
  groups: string[];
}

// Approvals (spec 019): the record of every destructive action.

export type ApprovalPhase = "Pending" | "Approved" | "Rejected" | "Expired" | "Executed" | "Failed";

export interface ApprovalAction {
  verb: "delete";
  target: Scope;
  cascade?: boolean;
}

export interface ApprovalSpec {
  action: ApprovalAction;
  reason?: string;
}

/** Who asked or decided: the audit actor, and the principal behind it when there is one. */
export interface ApprovalActor {
  actor: string;
  principal?: PrincipalRef;
  subject?: string;
  sponsor?: string;
}

/** One resource the action removes. Its last spec never leaves the server. */
export interface ApprovalRef {
  kind: string;
  id: string;
  name?: string;
  cleanedUp?: boolean;
}

export interface ApprovalDecision {
  byPolicy?: boolean;
  by?: ApprovalActor;
  at: string;
  comment?: string;
  evidence?: string;
}

export interface ApprovalStatus extends BaseStatus {
  phase: ApprovalPhase;
  requestedBy: ApprovalActor;
  requestedAt: string;
  expiresAt?: string;
  requestDecision?: string;
  targetName?: string;
  targetVersion?: number;
  ownerChain?: Scope[];
  blastRadius?: ApprovalRef[];
  decision?: ApprovalDecision;
  executedAt?: string;
  settled?: boolean;
}

export type Approval = Resource<ApprovalSpec, ApprovalStatus>;

/** GET /v1/graph/{kind}/{id}/children: core/graph.ResourceRef has no JSON tags. */
export interface GraphRef {
  Kind: string;
  ID: string;
}

export type SecretSpec = { path: string };
export type Secret = Resource<SecretSpec, BaseStatus>;

// Environments and deployments (spec 020): a Project is the subscription
// and an Environment the resource group a Component is deployed into.

export interface PromotionPath {
  from: string;
  to: string[];
}

export interface Pipeline {
  paths?: PromotionPath[];
}

export type Project = Resource<{ pipeline?: Pipeline }, BaseStatus>;

export type AvailabilityLevel = "single-instance" | "ha";
export type Environment = Resource<{ capabilityOverrides?: Record<string, string> }, BaseStatus & { availability?: AvailabilityLevel }>;

/** What differs in one Environment for the same release. */
export interface DeploymentOverrides {
  env?: Record<string, string>;
  config?: Record<string, unknown>;
  traits?: Record<string, Record<string, unknown>>;
}

export interface DeploymentSpec {
  component: string;
  /** Empty means the Component's latest release. */
  release?: string;
  overrides?: DeploymentOverrides;
}

export interface DeploymentStatus extends BaseStatus {
  release?: string;
  endpoints?: ComponentEndpoint[];
  /** Releases it ran before, newest first: what it may roll back to. */
  history?: string[];
}

export type Deployment = Resource<DeploymentSpec, DeploymentStatus>;

/** GET /v1/deployments/{id}/runtime */
export interface RuntimeObject {
  kind: string;
  name: string;
  /** Healthy, Progressing, Degraded, Suspended or Unknown. */
  health: string;
  message?: string;
}

/** GET /v1/deployments/{id}/events */
export interface RuntimeEvent {
  time: string;
  type: string;
  reason: string;
  message: string;
  objectKind: string;
  objectName: string;
  count?: number;
}

/** GET /v1/deployments/{id}/logs, and each line of ?follow=true. */
export interface LogLine {
  time: string;
  instance: string;
  line: string;
}

/** How a followed log stream ended on its own. */
export type LogEnd = "idle" | "maxDuration" | "ended" | "error";

/** A JSON Schema, as the platform publishes it. Only what the console reads is typed. */
export interface JSONSchema {
  $ref?: string;
  $defs?: Record<string, JSONSchema>;
  definitions?: Record<string, JSONSchema>;
  type?: string | string[];
  title?: string;
  description?: string;
  properties?: Record<string, JSONSchema>;
  required?: string[];
  enum?: unknown[];
  default?: unknown;
  minimum?: number;
  maximum?: number;
  minLength?: number;
  maxLength?: number;
  pattern?: string;
  items?: JSONSchema;
}

/** GET /v1/components/{id}/schemas */
export interface ConfigSchemas {
  environment?: JSONSchema;
  traits?: Record<string, JSONSchema>;
}

/** GET /v1/components/{id}/dependencies */
export interface ComponentLink {
  from: string;
  to: string;
  port: string;
  env: string;
}

export interface DependenciesView {
  calls: ComponentLink[];
  calledBy: ComponentLink[];
}

/** GET /v1/applications/{id}/topology: declared, not live traffic. */
export interface TopologyView {
  components: Array<{ id: string; name: string; ports: ComponentPort[] }>;
  dependencies: ComponentLink[];
}

/** GET /v1/platform */
export interface PlatformComponent {
  name: string;
  description?: string;
  parameters?: JSONSchema;
  environment?: JSONSchema;
  allowedTraits?: string[];
}

export interface PlatformView {
  runtimes: Array<{ name: string; ready: boolean; message?: string }>;
  componentTypes: PlatformComponent[];
  traits: PlatformComponent[];
}

/** GET /v1/packages */
export interface Package {
  provider: string;
  capability: string;
  description: string;
  available: boolean;
  /** Its measured cost; null until it's measured (spec 022). */
  requirements?: Requirements | null;
  /** What removing it loses, said before the request. */
  removal?: string;
  installable?: boolean;
  notInstallable?: string;
  /** An installed package's current disk use (spec 023), or why it couldn't be read. */
  usage?: DiskUsage;
  usageError?: string;
}

/** How much of its volumes an installed package uses now (core/reconcile.DiskUsage). capacityMiB 0: not known. */
export interface DiskUsage {
  usedMiB: number;
  capacityMiB: number;
  readAt: string;
}

/** What a package that keeps data writes to disk (core/provider.DiskRequirements). */
export interface DiskRequirements {
  growthMiBPerDay: number;
  retentionDays: number;
  budgetMiB: number;
}

/** A package's measured cost on the reference machine (core/provider.Requirements). */
export interface Requirements {
  idleMemoryMiB: number;
  idleCPUMillicores: number;
  peakMemoryMiB: number;
  peakCPUMillicores: number;
  measuredOn: string;
  results: string;
  note?: string;
  disk?: DiskRequirements;
}

export type BuildPhase = "Queued" | "Running" | "Succeeded" | "Failed" | "Cancelled";

export interface BuildStep {
  name: string;
  phase: BuildPhase;
  startedAt?: string;
  finishedAt?: string;
  message?: string;
}

/** GET /v1/components/{id}/builds and GET /v1/builds/{id} (spec 022). */
export type Build = Resource<
  { commit?: string; cancel?: boolean },
  BaseStatus & {
    phase?: BuildPhase;
    commit?: string;
    image?: string;
    startedAt?: string;
    finishedAt?: string;
    steps?: BuildStep[];
    source?: ComponentSource;
    started?: boolean;
  }
>;

export type ProviderInstance = Resource<{ capabilityId?: string; providerId?: string; environmentId?: string }, BaseStatus>;

/** A stored log line (spec 023, core/reconcile.StoredLogLine). component is the Component's ID. */
export interface StoredLogLine {
  time: string;
  component: string;
  instance: string;
  container?: string;
  line: string;
}

/**
 * One page of a stored-log search, newest first. Only an empty next ends a
 * search: a page can be short, or empty, with next set. omitted is on an
 * Application's search: Deployments left out because the caller may not
 * read their logs.
 */
export interface LogSearchPage {
  lines: StoredLogLine[];
  next?: string;
  truncated?: boolean;
  omitted?: number;
}

export interface MetricPoint {
  time: string;
  value: number;
}

export interface SeriesSummary {
  min: number;
  avg: number;
  max: number;
  latest: number;
  points: number;
}

/** A Deployment's CPU (millicores) and memory (MiB) over a range (GET /v1/deployments/{id}/metrics). */
export interface Metrics {
  cpu: MetricPoint[];
  memory: MetricPoint[];
  cpuRequest?: number;
  cpuLimit?: number;
  memoryRequest?: number;
  memoryLimit?: number;
  /** HTTP requests (spec 025), absent where they can't be read: unavailable says why. */
  requests?: RequestSeries;
  /** What the backend can't provide, each with the reason. */
  unavailable?: string[];
  from: string;
  to: string;
  summary: Partial<Record<"cpu" | "memory", SeriesSummary>>;
}

/** Requests and 5xx answers per second, and latency percentiles in milliseconds (core/reconcile RequestSeries). */
export interface RequestSeries {
  rate: MetricPoint[];
  errorRate: MetricPoint[];
  latencyP50: MetricPoint[];
  latencyP95: MetricPoint[];
  latencyP99: MetricPoint[];
}

// --- Networking and traces (spec 025): core/reconcile networking.go, tracing.go ---

/** GET /v1/networking */
export interface NetworkFacts {
  plugin: string;
  version?: string;
  policyEnforcement: "enforced" | "notEnforced" | "unknown";
  policyReason?: string;
  flows: boolean;
  requestMetrics: boolean;
  podRanges?: string[];
  unavailable?: string[];
  readAt: string;
}

/** One end of a connection record. */
export interface FlowEnd {
  component?: string;
  environment?: string;
  namespace?: string;
  workload?: string;
  external?: boolean;
  /** An end the caller isn't allowed to see: nothing else is set. */
  outside?: boolean;
}

export interface Flow {
  time: string;
  verdict: string;
  reason?: string;
  source: FlowEnd;
  destination: FlowEnd;
  protocol: string;
  port?: number;
  count: number;
}

/** GET /v1/deployments/{id}/flows */
export interface FlowPage {
  flows: Flow[] | null;
  next?: string;
  truncated?: boolean;
  from: string;
  to: string;
}

/** Requests and 5xx answers per second between two Components. */
export interface TrafficEdge {
  from: string;
  to: string;
  rate: number;
  errorRate: number;
}

/** GET /v1/applications/{id}/traffic */
export interface TrafficView {
  edges: TrafficEdge[] | null;
  from: string;
  to: string;
  omitted: number;
}

export interface TraceSummary {
  traceId: string;
  start: string;
  durationNs: number;
  root?: string;
  spans: number;
  hasErrors: boolean;
}

/** GET /v1/deployments/{id}/traces */
export interface TracePage {
  traces: TraceSummary[] | null;
  next?: string;
  truncated?: boolean;
  from: string;
  to: string;
}

export interface Span {
  spanId: string;
  parentId?: string;
  name: string;
  kind?: string;
  start: string;
  durationNs: number;
  failed: boolean;
  status?: string;
  component: string;
  environment: string;
  attributes?: Record<string, string>;
}

/** GET /v1/deployments/{id}/traces/{traceId} */
export interface TraceView {
  traceId: string;
  spans: Span[] | null;
  /** Some spans are from Components the caller can't read, and aren't shown. */
  partial: boolean;
  truncated?: boolean;
}

// --- Alerting (spec 024): core/resource/alerting.go ---

export type Severity = "info" | "warning" | "critical";

export interface MetricCondition {
  metric: "cpu" | "memory";
  as: "value" | "percentOfRequest" | "percentOfLimit";
  above: number;
  forMinutes: number;
}
export interface CountCondition {
  above: number;
  windowMinutes: number;
}
export interface AlertCondition {
  type: "metric" | "restarts" | "logMatch" | "packageDisk" | "requestErrorRate" | "requestLatency" | "traceErrors";
  metric?: MetricCondition;
  restarts?: CountCondition;
  logMatch?: CountCondition & { text: string };
  packageDisk?: { percentOfBudget: number };
  /** Spec 025: the network layer's request metrics. */
  requestErrorRate?: { abovePercent: number; minRequests?: number; windowMinutes: number };
  requestLatency?: { percentile: 95 | 99; aboveMs: number; windowMinutes: number };
  /** Spec 025: the tracing package. */
  traceErrors?: CountCondition;
}

export interface AlertRuleSpec {
  environmentId?: string;
  condition: AlertCondition;
  severity: Severity;
  summary: string;
  channelIds?: string[];
  intervalMinutes?: number;
  enabled?: boolean;
  builtIn?: boolean;
}
export interface AlertSilence {
  by: string;
  reason: string;
  until: string;
}
export interface AlertRuleStatus extends BaseStatus {
  active: boolean;
  activeReason?: string;
  lastEvaluated?: string;
  lastValue?: number;
  silence?: AlertSilence;
}
export type AlertRule = Resource<AlertRuleSpec, AlertRuleStatus>;

export interface AlertSpec {
  ruleId: string;
  componentId?: string;
  environmentId?: string;
  severity: Severity;
  summary: string;
  fingerprint: string;
}
export interface AlertDelivery {
  channelId: string;
  event: "firing" | "resolved";
  state: "pending" | "sent" | "failed" | "silenced" | "belowMinSeverity";
  attempts?: number;
  lastError?: string;
  at: string;
  nextAttempt?: string;
}
export interface AlertStatus extends BaseStatus {
  state: "firing" | "resolved";
  startedAt: string;
  resolvedAt?: string;
  resolvedReason?: string;
  value: number;
  threshold: number;
  acknowledged?: { by: string; at: string };
  deliveries?: AlertDelivery[];
}
export type Alert = Resource<AlertSpec, AlertStatus>;

/** GET /v1/alerts/summary: the caller's firing Alerts. */
export interface AlertSummary {
  firing: Partial<Record<Severity, number>>;
  total: number;
  unacknowledged: number;
}

export type ChannelType = "telegram" | "email" | "webhook";
export interface Allowance {
  reason: string;
}
export interface NotificationChannelSpec {
  type: ChannelType;
  telegram?: { chatId: string; botTokenSecretId: string };
  email?: { smtpServerId: string; to: string[] };
  webhook?: { url?: string; urlSecretId?: string; host?: string; signingSecretId?: string };
  minSeverity?: Severity;
  default?: boolean;
  allowPrivateDestination?: Allowance;
  allowPlainHTTP?: Allowance;
}
export interface NotificationChannelStatus extends BaseStatus {
  lastTest?: string;
  lastTestResult?: string;
}
/** A channel as the API shows it: each Secret only as whether it holds a value. */
export type NotificationChannel = Resource<NotificationChannelSpec, NotificationChannelStatus> & { secretSet?: Record<string, boolean> };
/** A platform default channel shown below "/": its settings hidden. */
export interface PlatformChannel {
  metadata: Metadata;
  spec: { type: ChannelType; minSeverity?: Severity; default: boolean };
  platformChannel: true;
}
export type ChannelListItem = NotificationChannel | PlatformChannel;

export interface SMTPServerSpec {
  host: string;
  port: number;
  tls: "starttls" | "implicit";
  from: string;
  usernameSecretId: string;
  passwordSecretId: string;
}
export type SMTPServer = Resource<SMTPServerSpec, BaseStatus> & { secretSet?: Record<string, boolean> };

export interface HeartbeatSpec {
  urlSecretId: string;
  host: string;
  method?: "GET" | "POST";
  intervalMinutes?: number;
  allowPrivateDestination?: Allowance;
}
export interface HeartbeatStatus extends BaseStatus {
  lastSent?: string;
  lastResult?: string;
}
export type Heartbeat = Resource<HeartbeatSpec, HeartbeatStatus> & { secretSet?: Record<string, boolean> };

/** POST /v1/secrets/{id}/telegramchats: chats the bot has seen a message from. */
export interface TelegramChat {
  id: string;
  type: string;
  title: string;
}
