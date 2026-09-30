import { LogoMark } from "./icons";

const rows = [
  { dot: "", nm: "apps", ns: "flux-system", ms: "Applied revision main@sha1:9f3c1a2", ago: "2m" },
  {
    dot: "bad",
    nm: "payments",
    ns: "flux-system",
    ms: "health check failed: Deployment/api",
    ago: "9m",
    sel: true,
  },
  { dot: "run", nm: "podinfo", ns: "demo", ms: "Reconciliation in progress", ago: "now" },
  {
    dot: "",
    nm: "infra-controllers",
    ns: "flux-system",
    ms: "Applied revision main@sha1:9f3c1a2",
    ago: "2m",
  },
  { dot: "off", nm: "batch-jobs", ns: "flux-system", ms: "Suspended", ago: "3d" },
  { dot: "", nm: "observability", ns: "monitoring", ms: "Applied revision main@sha1:9f3c1a2", ago: "2m" },
];

const sideNav = [
  { label: "Kustomizations", n: "14", cur: true },
  { label: "HelmReleases", n: "21", bad: true },
  { label: "Sources", n: "9" },
  { label: "Workloads", n: "58" },
  { label: "Threads", n: "3" },
];

/** An HTML/CSS illustration of the UI (styles in styles/mock.css). Decorative, so hidden from assistive tech. */
export function HeroMock() {
  return (
    <figure className="mx-auto mt-10 mb-0 max-w-[980px] sm:mt-14">
      <div
        className="win"
        role="img"
        aria-label="Illustration of the Eddy UI: a list of Flux resources in the staging cluster with the command palette open, searching for podinfo across clusters."
      >
        <div className="ribbon" />
        <div className="shell" aria-hidden="true">
          <div className="side">
            <div className="logo">
              <LogoMark />
              Eddy
            </div>
            <div className="sw-row">
              <div className="sw">S</div>
              <b>staging</b>
              <span>Staging · eu-west-1</span>
            </div>
            <div className="snav">
              {sideNav.map((item) => (
                <div key={item.label} className={item.cur ? "cur" : undefined}>
                  <span>{item.label}</span>
                  <span className={`n${item.bad ? " bad" : ""}`}>{item.n}</span>
                </div>
              ))}
            </div>
          </div>
          <div className="main">
            <div className="top">
              <div className="crumb">
                <span>staging /</span> Kustomizations
              </div>
              <div className="search">
                <span className="hint">Search all clusters</span> <kbd>⌘K</kbd>
              </div>
            </div>
            <div className="rows">
              <div className="row head">
                <span />
                <span>Name</span>
                <span>Namespace</span>
                <span>Status</span>
                <span>Age</span>
              </div>
              {rows.map((r) => (
                <div key={r.nm} className={`row${r.sel ? " sel" : ""}`}>
                  <span className={`dot ${r.dot}`} />
                  <span className="nm">{r.nm}</span>
                  <span className="ns">{r.ns}</span>
                  <span className="ms">{r.ms}</span>
                  <span className="ago">{r.ago}</span>
                </div>
              ))}
            </div>
          </div>
        </div>
        <div className="pal" aria-hidden="true">
          <div className="pal-in">
            <span>/</span>
            <span>podinfo</span>
            <span className="caret" />
          </div>
          <div className="pal-it sel">
            <code>Kustomization/podinfo</code>
            <span className="cl">staging</span>
          </div>
          <div className="pal-it">
            <code>HelmRelease/podinfo</code>
            <span className="cl pe">prod-eu</span>
          </div>
          <div className="pal-it">
            <code>Deployment/podinfo</code>
            <span className="cl dv">dev</span>
          </div>
          <div className="pal-foot">
            <span>
              <kbd>↑</kbd> <kbd>↓</kbd> move
            </span>
            <span>
              <kbd>↵</kbd> open
            </span>
            <span>
              <kbd>esc</kbd> close
            </span>
          </div>
        </div>
      </div>
      <figcaption className="fine mt-4 text-center">
        An illustration built in HTML and CSS, based on the UI design prototype. Each cluster gets its own
        colour.
      </figcaption>
    </figure>
  );
}
