import { createBrowserRouter, isRouteErrorResponse, Link, useRouteError } from "react-router";
import { Layout } from "./components/Layout";
import { AuditPage } from "./pages/AuditPage";
import { CandidatePage } from "./pages/CandidatePage";
import { CandidatesPage } from "./pages/CandidatesPage";
import { OverviewPage } from "./pages/OverviewPage";
import { PrPage } from "./pages/PrPage";
import { PrsPage } from "./pages/PrsPage";
import { ProposalsPage } from "./pages/ProposalsPage";

function NotFound() {
  const err = useRouteError();
  const missing = !err || (isRouteErrorResponse(err) && err.status === 404);
  return (
    <div className="page-head">
      <div>
        <h1 tabIndex={-1}>{missing ? "Nothing here" : "Something broke"}</h1>
        <p className="page-sub">
          {missing ? "That page doesn't exist." : String(err)} <Link to="/">Back to the overview</Link>
        </p>
      </div>
    </div>
  );
}

export const router = createBrowserRouter([
  {
    element: <Layout />,
    errorElement: <NotFound />,
    children: [
      { path: "/", element: <OverviewPage /> },
      { path: "/proposals", element: <ProposalsPage /> },
      { path: "/prs", element: <PrsPage /> },
      { path: "/prs/:id", element: <PrPage /> },
      { path: "/candidates", element: <CandidatesPage /> },
      { path: "/candidates/:slug", element: <CandidatePage /> },
      { path: "/audit", element: <AuditPage /> },
      { path: "*", element: <NotFound /> },
    ],
  },
]);
