// Deterministic fixtures for mock mode, ported from the design prototype:
// three connected clusters (prod-eu, staging, dev) plus a disconnected one.

import type { ClusterInfo, KubeEvent, Ref, Resource, Status } from "../api/types";
import { kindInfo } from "../lib/kinds";

let seed = 20260930;
const rnd = () => {
  seed = (seed * 16807) % 2147483647;
  return seed / 2147483647;
};
const ri = (a: number, b: number) => a + Math.floor(rnd() * (b - a + 1));
const pick = <T>(a: readonly T[]): T => a[Math.floor(rnd() * a.length)] as T;
const hex = (n: number) =>
  Array.from({ length: n }, () => "0123456789abcdef"[Math.floor(rnd() * 16)]).join("");
const sfx = (n: number) =>
  Array.from({ length: n }, () => "bcdfghjklmnpqrstvwxz2456789"[Math.floor(rnd() * 27)]).join("");

const NOW = Date.now();
const ago = (minutes: number) => new Date(NOW - minutes * 60_000).toISOString();
const randomAge = () => ago(pick([41, 180, 420, 1140, 2880, 5760, 15840, 33120]));

const VERSIONS: Record<string, string> = {
  Kustomization: "v1",
  HelmRelease: "v2",
  GitRepository: "v1",
  OCIRepository: "v1beta2",
  HelmRepository: "v1",
  HelmChart: "v1",
  Bucket: "v1",
  HorizontalPodAutoscaler: "v2",
};

export interface MockResource extends Resource {
  /** Mock-only: events shown on the Events tab. */
  events: KubeEvent[];
  /** Mock-only: extra spec fields rendered into the YAML tab. */
  spec: Record<string, string>;
}

export interface MockCluster {
  info: ClusterInfo;
  resources: Map<string, MockResource>;
}

let rv = 1000;
export const nextResourceVersion = (): string => String(++rv);

export const refOf = (r: Ref): Ref => ({
  group: r.group,
  kind: r.kind,
  namespace: r.namespace,
  name: r.name,
});

function make(
  kind: string,
  namespace: string,
  name: string,
  props: Partial<MockResource> = {},
): MockResource {
  const group = props.group ?? kindInfo(kind).group;
  const created = randomAge();
  return {
    group,
    kind,
    namespace,
    name,
    id: `${group}/${kind}/${namespace}/${name}`,
    version: VERSIONS[kind] ?? "v1",
    status: "ready",
    createdAt: created,
    lastChanged: created,
    resourceVersion: nextResourceVersion(),
    events: [],
    spec: {},
    ...props,
  };
}

const ev = (
  type: KubeEvent["type"],
  reason: string,
  message: string,
  minutesAgo: number,
  count = 1,
): KubeEvent => ({
  type,
  reason,
  message,
  count,
  source: "flux",
  first: ago(minutesAgo + 5),
  last: ago(minutesAgo),
});

const readyCondition = (status: Status, reason: string, message: string) => [
  {
    type: "Ready",
    status: status === "ready" ? "True" : status === "failed" ? "False" : "Unknown",
    reason,
    message,
    lastTransitionTime: ago(4),
  },
];

interface ClusterSeed {
  info: Omit<ClusterInfo, "counts">;
  branch: string;
}

const SEEDS: ClusterSeed[] = [
  {
    info: {
      name: "prod-eu",
      displayName: "prod-eu",
      environment: "Production",
      region: "eu-central-1",
      color: "#C2410C",
      protected: true,
      order: 1,
      connected: true,
      lastSeen: ago(0),
      agentVersion: "v1.0.0",
      kubernetesVersion: "v1.33.1",
      fluxVersion: "v2.7.0",
    },
    branch: "main",
  },
  {
    info: {
      name: "staging",
      displayName: "staging",
      environment: "Staging",
      region: "eu-west-1",
      color: "#6D28D9",
      protected: false,
      order: 2,
      connected: true,
      lastSeen: ago(0),
      agentVersion: "v1.0.0",
      kubernetesVersion: "v1.33.1",
      fluxVersion: "v2.7.0",
    },
    branch: "main",
  },
  {
    info: {
      name: "dev",
      displayName: "dev",
      environment: "Development",
      region: "kind, local",
      color: "#0F766E",
      protected: false,
      order: 3,
      connected: true,
      lastSeen: ago(0),
      agentVersion: "v1.0.0",
      kubernetesVersion: "v1.34.0",
      fluxVersion: "v2.7.1",
    },
    branch: "dev",
  },
  {
    info: {
      name: "edge-ap",
      displayName: "edge-ap",
      environment: "Edge",
      region: "ap-southeast-1",
      color: "#2563EB",
      protected: false,
      order: 4,
      connected: false,
      lastSeen: ago(47),
      agentVersion: "v1.0.0",
      kubernetesVersion: "v1.32.4",
      fluxVersion: "v2.6.4",
    },
    branch: "main",
  },
];

// Sidecars, so the log view's container picker has something to pick.
const SIDECARS: Record<string, string[]> = {
  podinfo: ["linkerd-proxy"],
  checkout: ["otel-agent"],
  redis: ["metrics"],
};

const CONTAINERS: Record<string, string> = {
  nginx: "controller",
  certmgr: "cert-manager",
  podinfo: "podinfo",
  redis: "redis",
  checkout: "checkout",
  loadgen: "worker",
};

function buildCluster(seedInfo: ClusterSeed, extraPods: number): MockCluster {
  const { info, branch } = seedInfo;
  const c = info.name;
  const prod = c === "prod-eu";
  const stg = c === "staging";
  const dev = c === "dev";
  const out = new Map<string, MockResource>();
  const add = (r: MockResource) => {
    out.set(r.id, r);
    return r;
  };

  const revision = `${branch}@sha1:${hex(40)}`;
  const git = add(
    make("GitRepository", "flux-system", "flux-system", {
      url: "ssh://git@github.com/acme/fleet-infra",
      revision,
      interval: "1m",
      message: `stored artifact for revision '${revision}'`,
      spec: { "ref.branch": branch },
    }),
  );
  const helmRepo = (name: string, url: string) =>
    add(
      make("HelmRepository", "flux-system", name, {
        url,
        interval: "1h",
        revision: `sha256:${hex(64)}`,
        message: "stored artifact: revision 'sha256:…'",
      }),
    );
  const rNginx = helmRepo("ingress-nginx", "https://kubernetes.github.io/ingress-nginx");
  const rJetstack = helmRepo("jetstack", "https://charts.jetstack.io");
  const rBitnami = helmRepo("bitnami", "oci://registry-1.docker.io/bitnamicharts");
  const tag = stg ? "6.7.2" : "6.7.1";
  const oci = add(
    make("OCIRepository", "flux-system", "podinfo", {
      url: "oci://ghcr.io/stefanprodan/charts/podinfo",
      interval: "10m",
      revision: `${tag}@sha256:${hex(64)}`,
      message: `stored artifact for digest '${tag}@sha256:…'`,
    }),
  );

  const ks = (name: string, path: string, interval: string, owner?: MockResource) =>
    add(
      make("Kustomization", "flux-system", name, {
        source: refOf(git),
        revision,
        interval,
        owner: owner ? refOf(owner) : undefined,
        message: `Applied revision: ${revision}`,
        spec: { path, prune: "true" },
      }),
    );
  const kRoot = ks("flux-system", `./clusters/${c}`, "10m");
  for (const src of [git, rNginx, rJetstack, rBitnami, oci]) src.owner = refOf(kRoot);
  const kInfra = ks("infra-controllers", "./infrastructure/controllers", "1h", kRoot);
  const kConfigs = ks("infra-configs", "./infrastructure/configs", "1h", kRoot);
  const kApps = ks("apps", `./apps/${c}`, "10m", kRoot);

  const hr = (ns: string, name: string, chart: string, source: MockResource, owner: MockResource) =>
    add(
      make("HelmRelease", ns, name, {
        chart,
        source: refOf(source),
        owner: refOf(owner),
        interval: "30m",
        revision: chart.split("@")[1],
        message: `Helm upgrade succeeded for release ${ns}/${name}.v${ri(2, 14)} with chart ${chart}`,
      }),
    );
  const hNginx = hr("ingress-nginx", "ingress-nginx", "ingress-nginx@4.12.1", rNginx, kInfra);
  const hCert = hr("cert-manager", "cert-manager", "cert-manager@v1.17.2", rJetstack, kInfra);
  const hPod = hr("apps", "podinfo", "podinfo@6.7.1", oci, kApps);
  const hRedis = dev ? null : hr("apps", "redis", "redis@20.11.3", rBitnami, kApps);

  const workload = (
    kind: "Deployment" | "StatefulSet" | "DaemonSet",
    ns: string,
    name: string,
    replicas: number,
    app: string,
    image: string,
    owner: MockResource,
  ) => {
    const w = add(
      make(kind, ns, name, {
        replicas: `${replicas}/${replicas}`,
        images: [image],
        owner: refOf(owner),
        message: `${replicas}/${replicas} replicas ready`,
        labels: { "app.kubernetes.io/name": app },
      }),
    );
    const hash = sfx(9);
    const pods: MockResource[] = [];
    for (let i = 0; i < replicas; i++) {
      const podName = kind === "StatefulSet" ? `${name}-${i}` : `${name}-${hash}-${sfx(5)}`;
      pods.push(
        add(
          make("Pod", ns, podName, {
            images: [image],
            containers: [CONTAINERS[app] ?? app, ...(SIDECARS[app] ?? [])],
            owner: refOf(w),
            message: "Running",
            labels: { "app.kubernetes.io/name": app },
            spec: {
              container: CONTAINERS[app] ?? app,
              nodeName: `ip-10-0-${ri(10, 200)}-${ri(2, 250)}.internal`,
            },
          }),
        ),
      );
    }
    return { w, pods };
  };

  const rep = prod ? 3 : stg ? 2 : 1;
  workload(
    "Deployment",
    "ingress-nginx",
    "ingress-nginx-controller",
    rep,
    "nginx",
    "registry.k8s.io/ingress-nginx/controller:v1.12.1",
    hNginx,
  );
  workload(
    "Deployment",
    "cert-manager",
    "cert-manager",
    1,
    "certmgr",
    "quay.io/jetstack/cert-manager-controller:v1.17.2",
    hCert,
  );
  workload(
    "Deployment",
    "cert-manager",
    "cert-manager-webhook",
    1,
    "certmgr",
    "quay.io/jetstack/cert-manager-webhook:v1.17.2",
    hCert,
  );
  workload(
    "Deployment",
    "cert-manager",
    "cert-manager-cainjector",
    1,
    "certmgr",
    "quay.io/jetstack/cert-manager-cainjector:v1.17.2",
    hCert,
  );
  const podinfo = workload(
    "Deployment",
    "apps",
    "podinfo",
    rep,
    "podinfo",
    `ghcr.io/stefanprodan/podinfo:${tag}`,
    hPod,
  );
  const checkoutTag = prod ? "2.14.0" : stg ? "2.15.0-rc.2" : `sha-${hex(7)}`;
  const checkout = workload(
    "Deployment",
    "apps",
    "checkout",
    prod ? 3 : 1,
    "checkout",
    `ghcr.io/acme/checkout:${checkoutTag}`,
    kApps,
  );
  workload(
    "DaemonSet",
    "kube-system",
    "node-exporter",
    prod ? 4 : 2,
    "node-exporter",
    "quay.io/prometheus/node-exporter:v1.9.1",
    kConfigs,
  );
  if (hRedis) {
    workload(
      "StatefulSet",
      "apps",
      "redis-master",
      1,
      "redis",
      "registry-1.docker.io/bitnami/redis:7.4.2",
      hRedis,
    );
    workload(
      "StatefulSet",
      "apps",
      "redis-replicas",
      prod ? 2 : 1,
      "redis",
      "registry-1.docker.io/bitnami/redis:7.4.2",
      hRedis,
    );
  }

  if (extraPods > 0) {
    const gen = workload(
      "Deployment",
      "load",
      "load-gen",
      Math.min(extraPods, 1),
      "loadgen",
      "ghcr.io/acme/load-gen:1.0.0",
      kApps,
    );
    gen.w.replicas = `${extraPods}/${extraPods}`;
    gen.w.message = `${extraPods}/${extraPods} replicas ready`;
    for (let i = 1; i < extraPods; i++) {
      const s = i % 97 === 0 ? "failed" : i % 41 === 0 ? "reconciling" : "ready";
      add(
        make("Pod", "load", `load-gen-${String(i).padStart(5, "0")}-${sfx(5)}`, {
          images: ["ghcr.io/acme/load-gen:1.0.0"],
          containers: ["worker"],
          owner: refOf(gen.w),
          status: s,
          message:
            s === "failed" ? "CrashLoopBackOff" : s === "reconciling" ? "ContainerCreating" : "Running",
          spec: { container: "worker", nodeName: `ip-10-1-${ri(0, 255)}-${ri(2, 250)}.internal` },
        }),
      );
    }
  }

  // Networking, batch, autoscaling and storage kinds (backend backlog #1).
  const svc = (ns: string, name: string, ports: string[], owner: MockResource, type = "ClusterIP") =>
    add(
      make("Service", ns, name, {
        owner: refOf(owner),
        ports,
        message:
          type === "LoadBalancer"
            ? `LoadBalancer ${ri(10, 99)}.${ri(10, 250)}.${ri(1, 250)}.${ri(2, 250)}`
            : type,
        spec: { type },
      }),
    );
  svc("ingress-nginx", "ingress-nginx-controller", ["80/TCP", "443/TCP"], hNginx, "LoadBalancer");
  svc("cert-manager", "cert-manager-webhook", ["443/TCP → 10250"], hCert);
  svc("apps", "podinfo", ["9898/TCP"], hPod);
  svc("apps", "checkout", ["8080/TCP"], kApps);
  if (hRedis) svc("apps", "redis-master", ["6379/TCP"], hRedis);
  const domain = `${c}.acme.dev`;
  add(
    make("Ingress", "apps", "podinfo", {
      owner: refOf(hPod),
      hosts: [`podinfo.${domain}`],
      message: `nginx · podinfo.${domain} · TLS`,
    }),
  );
  add(
    make("Ingress", "apps", "checkout", {
      owner: refOf(kApps),
      hosts: [`shop.${domain}`, `api.shop.${domain}`],
      message: `nginx · shop.${domain} · TLS`,
    }),
  );
  add(
    make("HorizontalPodAutoscaler", "apps", "podinfo", {
      owner: refOf(hPod),
      replicas: `${rep}/${rep}`,
      message: `${rep} replicas (min ${rep}, max ${rep * 3}), CPU ${ri(18, 60)}% of 80%`,
    }),
  );
  const cron = add(
    make("CronJob", "apps", "nightly-backup", {
      owner: refOf(kApps),
      schedule: "0 2 * * *",
      suspended: dev,
      status: dev ? "suspended" : "ready",
      message: dev ? "Suspended" : `Last scheduled ${ri(3, 20)}h ago`,
      images: ["ghcr.io/acme/backup:3.2.0"],
    }),
  );
  const job = (name: string, owner: MockResource, failed = false) => {
    const j = add(
      make("Job", "apps", name, {
        owner: refOf(owner),
        images: ["ghcr.io/acme/backup:3.2.0"],
        status: failed ? "failed" : "ready",
        replicas: failed ? "0/1" : "1/1",
        message: failed
          ? "BackoffLimitExceeded: Job has reached the specified backoff limit"
          : "Complete, 1/1 succeeded",
      }),
    );
    add(
      make("Pod", "apps", `${name}-${sfx(5)}`, {
        owner: refOf(j),
        images: ["ghcr.io/acme/backup:3.2.0"],
        containers: ["backup"],
        status: failed ? "failed" : "ready",
        message: failed ? "Error" : "Completed",
        spec: { container: "backup", nodeName: `ip-10-0-${ri(10, 200)}-${ri(2, 250)}.internal` },
      }),
    );
    return j;
  };
  job(`nightly-backup-${ri(29260000, 29269999)}`, cron);
  if (stg) job("db-migrate-2-15-0", kApps, true);
  if (hRedis) {
    add(
      make("PersistentVolumeClaim", "apps", "redis-data-redis-master-0", {
        owner: refOf(hRedis),
        message: "Bound · 8Gi · gp3",
      }),
    );
  }

  // Inventory-only entries: objects Flux applied whose kinds Eddy does not watch. Only kind,
  // namespace and name are known (never any ConfigMap or Secret data).
  const inv = (kind: string, group: string, ns: string, name: string, owner: MockResource) =>
    add(make(kind, ns, name, { group, owner: refOf(owner), status: "unknown", inventoryOnly: true }));
  inv("Namespace", "", "", "apps", kApps);
  inv("ServiceAccount", "", "apps", "checkout", kApps);
  inv("ConfigMap", "", "apps", "checkout-config", kApps);
  inv("Secret", "", "apps", "checkout-db", kApps);
  inv("ServiceAccount", "", "apps", "podinfo", hPod);
  inv("Namespace", "", "", "cert-manager", kInfra);
  inv("CustomResourceDefinition", "apiextensions.k8s.io", "", "certificates.cert-manager.io", hCert);
  inv("ClusterRole", "rbac.authorization.k8s.io", "", "cert-manager-controller-certificates", hCert);
  inv("ClusterIssuer", "cert-manager.io", "", "letsencrypt-prod", kConfigs);

  // Scenario tweaks from the prototype.
  if (prod) {
    const d = checkout.w;
    Object.assign(d, {
      status: "reconciling",
      replicas: "2/3",
      images: ["ghcr.io/acme/checkout:2.14.1"],
      message: "Rolling out: 2 of 3 updated replicas available",
      lastChanged: ago(1),
    });
    const p = checkout.pods[2];
    if (p) {
      Object.assign(p, {
        status: "reconciling",
        message: "ContainerCreating",
        images: ["ghcr.io/acme/checkout:2.14.1"],
        createdAt: ago(1),
      });
      p.events = [ev("Normal", "Pulling", 'Pulling image "ghcr.io/acme/checkout:2.14.1"', 1)];
    }
  }
  if (stg) {
    Object.assign(hPod, {
      status: "failed",
      chart: "podinfo@6.7.2",
      revision: "6.7.1",
      message:
        "Helm upgrade failed for release apps/podinfo with chart podinfo@6.7.2: context deadline exceeded",
      lastChanged: ago(6),
    });
    hPod.conditions = readyCondition("failed", "UpgradeFailed", hPod.message ?? "");
    hPod.events = [
      ev("Warning", "UpgradeFailed", hPod.message ?? "", 1, 3),
      ev("Normal", "Progressing", "Reconciliation started", 16),
      ev(
        "Normal",
        "UpgradeSucceeded",
        "Helm upgrade succeeded for release apps/podinfo with chart podinfo@6.7.1",
        4320,
      ),
    ];
    Object.assign(podinfo.w, { status: "reconciling", replicas: "1/2", message: "1/2 replicas ready" });
    const p = podinfo.pods[1];
    if (p) {
      Object.assign(p, {
        status: "failed",
        message: "ImagePullBackOff",
        images: ["ghcr.io/stefanprodan/podinfo:6.7.2-debug"],
        createdAt: ago(6),
      });
      p.events = [
        ev("Warning", "BackOff", 'Back-off pulling image "ghcr.io/stefanprodan/podinfo:6.7.2-debug"', 0, 14),
        ev(
          "Warning",
          "Failed",
          'Failed to pull image "ghcr.io/stefanprodan/podinfo:6.7.2-debug": manifest unknown',
          6,
        ),
        ev("Normal", "Scheduled", "Successfully assigned apps/podinfo to ip-10-0-31-7.internal", 6),
      ];
    }
    Object.assign(kApps, {
      status: "failed",
      message:
        "Health check failed after 5m0s: timeout waiting for: [HelmRelease/apps/podinfo status: 'Failed']",
      lastChanged: ago(5),
    });
    kApps.conditions = readyCondition("failed", "HealthCheckFailed", kApps.message ?? "");
    kApps.events = [ev("Warning", "HealthCheckFailed", kApps.message ?? "", 5, 2)];
  }
  if (dev) {
    Object.assign(kApps, { status: "suspended", suspended: true, message: "Reconciliation is suspended" });
    kApps.events = [ev("Normal", "Suspended", "Reconciliation suspended by alex@acme.dev", 2880)];
    for (const r of out.values()) {
      if (r.kind === "HelmRelease" && r.name === "podinfo") {
        Object.assign(r, {
          status: "failed",
          message: "Chart pull error: no 'podinfo' chart with version matching '6.8.x' found",
          chart: "podinfo@6.8.x",
        });
      }
    }
  }

  // Default conditions and events for everything not customised above.
  for (const r of out.values()) {
    if ((r.lastChanged ?? "") < (r.createdAt ?? "")) r.lastChanged = r.createdAt;
    if (r.inventoryOnly) continue;
    if (kindInfo(r.kind).flux && !r.conditions) {
      r.conditions = readyCondition(
        r.status,
        r.status === "ready" ? "ReconciliationSucceeded" : "Progressing",
        r.message ?? "",
      );
    }
    if (r.events.length === 0) {
      if (r.kind === "Pod") {
        const image = r.images?.[0] ?? "";
        r.events = [
          ev("Normal", "Started", `Started container ${r.spec.container ?? ""}`, 60),
          ev("Normal", "Pulled", `Container image "${image}" already present on machine`, 60),
          ev(
            "Normal",
            "Scheduled",
            `Successfully assigned ${r.namespace}/${r.name} to ${r.spec.nodeName ?? ""}`,
            61,
          ),
        ];
      } else if (kindInfo(r.kind).flux) {
        const reason =
          r.kind === "HelmRelease"
            ? "UpgradeSucceeded"
            : r.kind === "Kustomization"
              ? "ReconciliationSucceeded"
              : "NewArtifact";
        r.events = [ev("Normal", reason, r.message ?? "", 4)];
      } else {
        r.events = [
          ev(
            "Normal",
            "ScalingReplicaSet",
            `Scaled up replica set ${r.name}-${sfx(9)} to ${r.replicas?.split("/")[1] ?? 1}`,
            180,
          ),
        ];
      }
    }
  }
  return { info: { ...info, counts: countsOf(out) }, resources: out };
}

export function countsOf(resources: Map<string, Resource>): Partial<Record<Status, number>> {
  const counts: Partial<Record<Status, number>> = {};
  for (const r of resources.values()) {
    if (!r.inventoryOnly) counts[r.status] = (counts[r.status] ?? 0) + 1;
  }
  return counts;
}

export function buildFleet(extraPods: number): MockCluster[] {
  return SEEDS.map((s) => buildCluster(s, s.info.name === "dev" ? extraPods : 0));
}

/** A readable, redacted-looking manifest for the YAML tab. */
export function yamlOf(r: MockResource): string {
  const api = r.group ? `${r.group}/${r.version}` : r.version;
  const lines = [`apiVersion: ${api}`, `kind: ${r.kind}`, "metadata:", `  name: ${r.name}`];
  if (r.namespace) lines.push(`  namespace: ${r.namespace}`);
  if (r.labels) {
    lines.push("  labels:");
    for (const [k, v] of Object.entries(r.labels)) lines.push(`    ${k}: ${v}`);
  }
  lines.push("spec:");
  if (r.interval) lines.push(`  interval: ${r.interval}`);
  if (r.url) lines.push(`  url: ${r.url}`);
  for (const [k, v] of Object.entries(r.spec)) lines.push(`  ${k}: ${v}`);
  if (r.source) lines.push("  sourceRef:", `    kind: ${r.source.kind}`, `    name: ${r.source.name}`);
  if (r.chart)
    lines.push(
      "  chart:",
      `    name: ${r.chart.split("@")[0]}`,
      `    version: "${r.chart.split("@")[1] ?? ""}"`,
    );
  if (r.images) {
    lines.push("  containers:");
    for (const image of r.images) lines.push(`  - image: ${image}`);
  }
  if (kindInfo(r.kind).flux) lines.push(`  suspend: ${Boolean(r.suspended)}`);
  lines.push("status:");
  for (const c of r.conditions ?? []) {
    lines.push(
      "  conditions:",
      `  - type: ${c.type}`,
      `    status: "${c.status}"`,
      `    reason: ${c.reason ?? ""}`,
    );
    if (c.message) lines.push(`    message: "${c.message.replace(/"/g, '\\"')}"`);
  }
  if (r.revision) lines.push(`  lastAppliedRevision: ${r.revision}`);
  if (r.replicas)
    lines.push(`  readyReplicas: ${r.replicas.split("/")[0]}`, `  replicas: ${r.replicas.split("/")[1]}`);
  return lines.join("\n");
}

const LOG_TEMPLATES: Record<string, () => string> = {
  nginx: () => {
    const code = pick([200, 200, 200, 200, 201, 204, 304, 404, 499, 502]);
    return `${code >= 500 ? "error" : code >= 400 ? "warn" : "info"} 10.0.${ri(0, 255)}.${ri(2, 254)} "${pick(["GET", "GET", "POST"])} ${pick(["/", "/api/cart", "/api/checkout", "/healthz", "/static/app.js"])} HTTP/2.0" ${code} ${ri(120, 48000)}B ${ri(1, 240)}ms`;
  },
  podinfo: () =>
    rnd() < 0.08
      ? "warn slow request path=/api/delay/2 duration=2.01s"
      : `info ${pick(["request path=/healthz status=200 duration=0.4ms", "request path=/readyz status=200 duration=0.3ms", "request path=/api/info status=200 duration=1.2ms", "cache hit key=info ttl=30s"])}`,
  checkout: () => {
    const r = rnd();
    if (r < 0.06)
      return `error payment provider error: upstream timeout after 3s, order=ord_${sfx(6)} retrying`;
    if (r < 0.16) return `warn payment provider slow p95=${ri(700, 1200)}ms`;
    return `info ${pick([`order placed id=ord_${sfx(6)} items=${ri(1, 5)}`, `cart updated session=${sfx(8)} items=${ri(1, 6)}`, "health check ok redis=up db=up"])}`;
  },
  redis: () =>
    `info ${pick(["Background saving started", "DB saved on disk", "Synchronization with replica succeeded", `${ri(1, 99)} changes in 300 seconds. Saving...`])}`,
};

export function logLine(app: string | undefined): string {
  const t = new Date().toISOString();
  const gen = LOG_TEMPLATES[app ?? ""];
  return `${t} ${gen ? gen() : `info ${pick(["certificate does not need re-issuance", "leader election renewed lease", "nothing to do", "sync complete"])}`}`;
}
