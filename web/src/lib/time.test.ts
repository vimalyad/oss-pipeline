import { relative } from "./time";

describe("relative", () => {
  const now = new Date("2026-10-03T12:00:00Z");
  it("rounds to the largest whole unit", () => {
    expect(relative("2026-10-03T09:00:00Z", now)).toBe("3 hr. ago");
    expect(relative("2026-10-02T12:00:00Z", now)).toBe("yesterday");
    expect(relative("2026-09-19T12:00:00Z", now)).toBe("2 wk. ago");
  });
  it("does not report seconds", () => {
    expect(relative("2026-10-03T11:59:30Z", now)).toBe("just now");
  });
});
