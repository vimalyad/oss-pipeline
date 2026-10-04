import { lastWeeks, niceMax } from "./weeks";

describe("lastWeeks", () => {
  it("fills empty weeks with zeros, oldest first, Monday-aligned", () => {
    const now = new Date("2026-10-03T12:00:00Z"); // a Saturday
    const weeks = lastWeeks(
      [{ week: "2026-09-21", opened: 3, merged: 1, closed: 0, proposed: 5, approved: 4 }],
      3,
      now,
    );
    expect(weeks.map((w) => w.week)).toEqual(["2026-09-14", "2026-09-21", "2026-09-28"]);
    expect(weeks.map((w) => w.opened)).toEqual([0, 3, 0]);
  });
});

describe("niceMax", () => {
  it("rounds up to a clean tick", () => {
    expect(niceMax(7)).toBe(10);
    expect(niceMax(11)).toBe(20);
    expect(niceMax(0)).toBe(5);
  });
});
