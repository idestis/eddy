import { createRootRoute, Link, Outlet } from "@tanstack/react-router";
import { Footer } from "../components/Footer";
import { Header } from "../components/Header";
import { pageHead } from "../lib/head";

// The root head is the 404 default; every real route supplies its own.
export const Route = createRootRoute({
  head: () =>
    pageHead({
      title: "Page not found · Eddy",
      description: "That page does not exist, or it moved.",
      noindex: true,
    }),
  component: RootLayout,
  notFoundComponent: NotFound,
});

function RootLayout() {
  return (
    <>
      <a
        href="#main"
        className="absolute top-2.5 left-4 z-30 -translate-y-16 rounded-xl bg-accent px-4 py-2 text-on-accent focus:translate-y-0"
      >
        Skip to content
      </a>
      <Header />
      <main id="main" tabIndex={-1} className="outline-none">
        <Outlet />
      </main>
      <Footer />
    </>
  );
}

function NotFound() {
  return (
    <section className="wrap py-[16vh] text-center">
      <p className="mb-3 font-mono text-[0.78rem] font-semibold tracking-[0.08em] text-accent uppercase">
        404
      </p>
      <h1 className="mb-3 text-4xl font-bold">Nothing here</h1>
      <p className="mx-auto mb-7 max-w-[38em] text-lg text-ink-2">That page does not exist, or it moved.</p>
      <div className="flex flex-wrap justify-center gap-3">
        <Link to="/" className="btn btn-primary">
          Back to Eddy
        </Link>
        <Link to="/docs/" className="btn btn-secondary">
          Read the docs
        </Link>
      </div>
    </section>
  );
}
