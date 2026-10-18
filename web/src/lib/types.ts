// Mirrors the hub API (hub/api) and proto messages (proto/nexos/v1).
// Proto fields use omitempty, so zero values arrive as undefined.

export type Role = "admin" | "operator" | "viewer";
export interface User {
  id: number;
  username: string;
  role: Role;
}
export interface Me {
  user: User;
  csrf_token: string;
}

export type HostStatus = "pending" | "connecting" | "online" | "offline";

export interface DockerInfo {
  available?: boolean;
  error?: string;
  server_version?: string;
  api_version?: string;
  containers_total?: number;
  containers_running?: number;
  containers_paused?: number;
  containers_stopped?: number;
  images?: number;
  storage_driver?: string;
}

export interface HostInfo {
  hostname?: string;
  os?: string;
  platform?: string;
  platform_version?: string;
  kernel_version?: string;
  arch?: string;
  uptime_seconds?: number;
  boot_time_unix?: number;
  cpu_model?: string;
  cpu_cores_physical?: number;
  cpu_cores_logical?: number;
  memory_total_bytes?: number;
  agent_version?: string;
  docker?: DockerInfo;
}

export interface DiskUsage {
  mountpoint?: string;
  device?: string;
  fstype?: string;
  total_bytes?: number;
  used_bytes?: number;
  free_bytes?: number;
  used_percent?: number;
}

export interface NetIface {
  name?: string;
  rx_bytes_total?: number;
  tx_bytes_total?: number;
  rx_bytes_per_second?: number;
  tx_bytes_per_second?: number;
  rx_errors?: number;
  tx_errors?: number;
  virtual?: boolean;
}

export interface HostMetrics {
  timestamp_unix_ms?: number;
  cpu_percent?: number;
  cpu_per_core_percent?: number[];
  load1?: number;
  load5?: number;
  load15?: number;
  memory_total_bytes?: number;
  memory_used_bytes?: number;
  memory_available_bytes?: number;
  swap_total_bytes?: number;
  swap_used_bytes?: number;
  disks?: DiskUsage[];
  network?: NetIface[];
  temperatures?: { sensor?: string; celsius?: number }[];
  uptime_seconds?: number;
  network_rx_bytes_per_second?: number;
  network_tx_bytes_per_second?: number;
  disk_read_bytes_per_second?: number;
  disk_write_bytes_per_second?: number;
}

export interface Host {
  id: string;
  name: string;
  address: string;
  hostname?: string;
  created_at: string;
  enrolled_at?: string;
  last_seen_at?: string;
  status: HostStatus;
  status_since?: string;
  error?: string;
  info?: HostInfo;
  metrics?: HostMetrics;
  enrollment_expires_at?: string;
  cert_not_after?: string;
}

export interface MetricPoint {
  ts: number;
  cpu: number;
  mem_used: number;
  mem_total: number;
  swap_used: number;
  load1: number;
  net_rx: number;
  net_tx: number;
  disk_read: number;
  disk_write: number;
}

export interface AuditEntry {
  id: number;
  time: string;
  user_id?: number;
  username?: string;
  ip?: string;
  action: string;
  target?: string;
  agent_id?: string;
  success: boolean;
  detail?: string;
}

export interface Enrollment {
  bundle: string;
  expires_at: string;
}

export type LiveEvent =
  | { type: "host.metrics"; host_id: string; data: HostMetrics }
  | { type: "host.info"; host_id: string; data: HostInfo }
  | { type: "host.status"; host_id: string; data: { status: HostStatus; error?: string; since: string } }
  | { type: "docker.event"; host_id: string; data: DockerEvent };

export interface ContainerPort {
  ip?: string;
  private_port?: number;
  public_port?: number;
  type?: string;
}

export interface ContainerMount {
  type?: string;
  source?: string;
  destination?: string;
  read_only?: boolean;
  name?: string;
}

export type ContainerState = "created" | "running" | "paused" | "restarting" | "removing" | "exited" | "dead";

export interface Container {
  id: string;
  name: string;
  image?: string;
  image_id?: string;
  command?: string;
  created_unix?: number;
  state: ContainerState;
  status?: string;
  health?: string;
  ports?: ContainerPort[];
  labels?: Record<string, string>;
  networks?: string[];
  mounts?: ContainerMount[];
  compose_project?: string;
  compose_service?: string;
  protected?: boolean;
}

export interface ContainerStats {
  id: string;
  name?: string;
  cpu_percent?: number;
  memory_usage_bytes?: number;
  memory_limit_bytes?: number;
  memory_percent?: number;
  network_rx_bytes?: number;
  network_tx_bytes?: number;
  network_rx_bytes_per_second?: number;
  network_tx_bytes_per_second?: number;
  block_read_bytes?: number;
  block_write_bytes?: number;
  pids?: number;
}

export interface StatsMessage {
  timestamp_unix_ms?: number;
  stats?: ContainerStats[];
  error?: string;
}

export interface LogLine {
  stream?: "stdout" | "stderr";
  text?: string;
}

export type ContainerAction = "start" | "stop" | "restart" | "kill" | "pause" | "unpause";

export interface DockerEvent {
  type?: string;
  action?: string;
  actor_id?: string;
  attributes?: Record<string, string>;
  time_unix_nano?: number;
}
