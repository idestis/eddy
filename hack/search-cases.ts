// Regenerates testdata/search_cases.json from web/src/lib/fuzzy.ts, the
// source of truth for the palette ranking that internal/hub/fuzzy.go ports.
//   node --experimental-strip-types hack/search-cases.ts testdata/search_cases.json

import { matchField, matchItem, rank } from "../web/src/lib/fuzzy.ts";
import { writeFileSync } from "node:fs";

const fieldInputs: Array<[string, string, boolean]> = [
  ["redis", "redis", true], ["red", "redis", true], ["sys", "flux-system", true], ["ystem", "flux-system", true],
  ["cm", "cert-manager", true], ["fs", "flux-system", false], ["flux-sy", "flux-system", false],
  ["fdp", "podinfo", true], ["xyz", "podinfo", true], ["oif", "podinfo", true],
  ["redis", "redis-master", true], ["redis", "redis-replicas-long-name", true],
  ["REDIS", "Redis-Master", true], ["master", "redis-master", false], ["ks", "KS", false],
  ["hr", "HelmRelease", false], ["helm", "HelmRelease", false], ["release", "HelmRelease", false],
  ["nightly", "billing-api-nightly-29004096", true], ["29004", "billing-api-nightly-29004096", true],
  ["ban", "billing-api-nightly-29004096", true], ["bapi", "billing-api-nightly-29004096", true],
  ["api", "", true], ["a", "a", true], ["a", "ba", true], ["kube", "kube-system", false],
  ["team-03", "team-03-billing-1", false], ["0", "team-03-billing-1", false],
  ["gw", "gateway-7d9f-x2", true], ["gwx", "gateway-7d9f-x2", true], ["g7x", "gateway-7d9f-x2", true],
  ["pod", "podinfo-6b8d9c-abcde", true], ["info", "podinfo", true], ["podinfoextra", "podinfo", true],
  ["@", "a@b", true], ["b.c", "a.b.c", true], ["c", "a/b:c", true],
];

const fieldCases = fieldInputs.map(([term, text, primary]) => ({ term, text, primary, match: matchField(term, text, primary) }));

const itemInputs: Array<{ query: string; primary: string; secondary: string[] }> = [
  { query: "pod staging", primary: "podinfo", secondary: ["apps", "HelmRelease", "HR", "staging"] },
  { query: "pod prod", primary: "podinfo", secondary: ["apps", "HelmRelease", "HR", "staging"] },
  { query: "", primary: "anything", secondary: [] },
  { query: "  hr   podinfo ", primary: "podinfo", secondary: ["apps", "HelmRelease", "HR", "prod-eu"] },
  { query: "billing nightly", primary: "billing-api-nightly-29004096", secondary: ["team-03-billing-1", "Job", "JOB", "lg-003"] },
  { query: "job team-03", primary: "billing-api-nightly-29004096", secondary: ["team-03-billing-1", "Job", "JOB", "lg-003"] },
  { query: "ks flux", primary: "apps", secondary: ["flux-system", "Kustomization", "KS", "dev"] },
  { query: "bapi lg", primary: "billing-api-nightly-29004096", secondary: ["team-03-billing-1", "Job", "JOB", "lg-003"] },
  { query: "zzz", primary: "podinfo", secondary: ["apps", "HelmRelease", "HR", "staging"] },
  { query: "deploy", primary: "web", secondary: ["shop", "Deployment", "DEP", "prod"] },
];
const itemCases = itemInputs.map((c) => ({ ...c, match: matchItem(c.query, { primary: c.primary, secondary: c.secondary }) }));

interface Item { id: string; primary: string; secondary: string[]; order: number }
const mk = (cluster: string, kind: string, abbr: string, ns: string, name: string, order: number): Item => ({
  id: `${cluster}/${kind}/${ns}/${name}`, primary: name, secondary: [ns, kind, abbr, cluster], order,
});
// order = statusRank * 2 + (cluster === current ? 0 : 1); lower first.
const items: Item[] = [
  mk("prod", "HelmRelease", "HR", "apps", "podinfo", 4 * 2 + 1),
  mk("staging", "HelmRelease", "HR", "apps", "podinfo", 4 * 2 + 0),
  mk("dev", "HelmRelease", "HR", "apps", "podinfo", 0 * 2 + 1),
  mk("prod", "Deployment", "DEP", "apps", "podinfo-frontend", 4 * 2 + 1),
  mk("prod", "Pod", "POD", "apps", "podinfo-6b8d9c-abcde", 5 * 2 + 1),
  mk("prod", "Kustomization", "KS", "flux-system", "apps", 1 * 2 + 1),
  mk("staging", "Kustomization", "KS", "flux-system", "infra-controllers", 4 * 2 + 0),
  mk("prod", "Service", "SVC", "redis", "redis-master", 4 * 2 + 1),
  mk("prod", "StatefulSet", "STS", "redis", "redis-replicas-long-name", 0 * 2 + 1),
  mk("dev", "ConfigMap", "CM", "apps", "podinfo-config", 9 * 2 + 1),
  mk("lg-003", "Job", "JOB", "team-03-billing-1", "billing-api-nightly-29004096", 5 * 2 + 1),
  mk("lg-003", "Job", "JOB", "team-03-billing-1", "billing-api-nightly-29008192", 0 * 2 + 1),
  mk("lg-004", "CronJob", "CJ", "team-04-search-1", "search-sync-nightly", 4 * 2 + 1),
];
const rankQueries = ["podinfo", "pod", "redis", "apps", "ks", "nightly", "billing nightly", "pdi", "hr apps", "prod pod", "sync", "zzz", "p"];
const rankCases = rankQueries.map((query) => ({
  query, limit: 30,
  expected: rank(query, items, (i) => ({ primary: i.primary, secondary: i.secondary }), 30, (a, b) => a.order - b.order).map((r) => r.item.id),
}));
const limited = { query: "pod", limit: 3, expected: rank("pod", items, (i) => ({ primary: i.primary, secondary: i.secondary }), 3, (a, b) => a.order - b.order).map((r) => r.item.id) };

writeFileSync(process.argv[2], JSON.stringify({
  description: "Shared cases for web/src/lib/fuzzy.ts and internal/hub/fuzzy.go (ADR-0006). Generated from fuzzy.ts; both test suites must pass them. rank items tiebreak on `order` (lower first): statusRank*2 + (cluster is current ? 0 : 1).",
  fieldCases, itemCases, rankItems: items, rankCases: [...rankCases, limited],
}, null, 2) + "\n");
