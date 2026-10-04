import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Decide } from "./Decide";

function renderDecide(status: "proposed" | "pr_open" = "proposed") {
  const qc = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <Decide slug="org__repo__1" status={status} />
    </QueryClientProvider>,
  );
}

const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

afterEach(() => vi.restoreAllMocks());

describe("Decide", () => {
  it("will not send a rejection without a reason", async () => {
    const fetch = vi.spyOn(globalThis, "fetch");
    renderDecide();
    await userEvent.click(screen.getByRole("button", { name: /reject/i }));
    expect(screen.getByLabelText(/why are you rejecting/i)).toHaveFocus();
    expect(screen.getByRole("button", { name: "Reject" })).toBeDisabled();
    await userEvent.type(screen.getByLabelText(/why are you rejecting/i), "   ");
    expect(screen.getByRole("button", { name: "Reject" })).toBeDisabled();
    expect(fetch).not.toHaveBeenCalled();
  });

  it("sends the trimmed reason and reports what the server did", async () => {
    const fetch = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      json(200, { slug: "org__repo__1", status: "rejected", changed: true, message: "rejected org__repo__1" }),
    );
    renderDecide();
    await userEvent.click(screen.getByRole("button", { name: /reject/i }));
    await userEvent.type(screen.getByLabelText(/why are you rejecting/i), "  out of scope ");
    await userEvent.click(screen.getByRole("button", { name: "Reject" }));
    expect(await screen.findByText("rejected org__repo__1")).toBeInTheDocument();
    const [url, init] = fetch.mock.calls[0]!;
    expect(url).toBe("/api/candidates/org__repo__1/reject");
    expect(JSON.parse(String(init?.body))).toEqual({ reason: "out of scope" });
  });

  it("shows the server's refusal verbatim rather than pretending it worked", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      json(409, { title: "Conflict", detail: "org__repo__1 is approved, not awaiting approval" }),
    );
    renderDecide();
    await userEvent.click(screen.getByRole("button", { name: /approve/i }));
    expect(await screen.findByText("org__repo__1 is approved, not awaiting approval")).toBeInTheDocument();
  });

  it("offers nothing once a pull request is open", () => {
    const { container } = renderDecide("pr_open");
    expect(container).toBeEmptyDOMElement();
  });
});
