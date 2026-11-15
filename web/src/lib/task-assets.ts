import type { NewAssetType } from "@/lib/types";

const ASSET_TYPE_LABELS: Record<NewAssetType, string> = {
  app: "Application",
  endpoint: "Endpoint",
  ip: "IP",
  root_domain: "Root Domain",
  service: "Service",
  subdomain: "Subdomain",
};

const TASK_ASSET_SOURCE_LABELS: Record<string, string> = {
  agent: "Agent Discovery",
  anchor: "Blackboard Anchor",
  api: "Asset API",
  company: "Company Scope",
  legacy: "Legacy Link",
  manual: "Manual Addition",
  system: "System Link",
  task: "Task Initialization",
};

export function taskAssetTypeLabel(type: NewAssetType): string {
  return ASSET_TYPE_LABELS[type];
}

export function taskAssetSourceLabel(source: string): string {
  return TASK_ASSET_SOURCE_LABELS[source] ?? source;
}
