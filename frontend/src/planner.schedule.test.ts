import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const { scheduleMock } = vi.hoisted(() => ({ scheduleMock: vi.fn(async () => ({})) }));

vi.mock("@wailsio/runtime", () => ({
    Events: { On: vi.fn(() => () => {}), Off: vi.fn(), Once: vi.fn(), Emit: vi.fn() },
}));

vi.mock("../bindings/quire/plannerservice.js", () => ({
    Plan: vi.fn(async () => ({ version: 1, noteId: "note-1", updated: 0, slots: [] })),
    Schedule: scheduleMock,
    Reschedule: vi.fn(async () => ({})),
    Queue: vi.fn(async () => []),
    Backlog: vi.fn(async () => []),
    History: vi.fn(async () => []),
}));

import { openSchedulePopover } from "./planner.js";

const MONTH = 0;
const DAY = 1;
const YEAR = 2;
const HOUR = 3;
const MINUTE = 4;

function boxes(): HTMLInputElement[] {
    return Array.from(document.querySelectorAll<HTMLInputElement>(".plan-num"));
}

function put(index: number, value: string): void {
    const box = boxes()[index];
    box.value = value;
    box.dispatchEvent(new Event("input", { bubbles: true }));
}

function press(index: number, key: string): void {
    boxes()[index].dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true }));
}

function chooserMeridiem(value: "AM" | "PM"): void {
    Array.from(document.querySelectorAll<HTMLElement>(".plan-ampm button"))
        .find((node) => node.textContent === value)
        ?.click();
}

beforeEach(async () => {
    document.body.replaceChildren();
    // planner.ts keeps document-level shortcuts that read #planner-view; the app
    // always mounts it, the test has to.
    const plannerView = document.createElement("div");
    plannerView.id = "planner-view";
    plannerView.hidden = true;
    document.body.append(plannerView);
    scheduleMock.mockClear();
    await openSchedulePopover({ noteId: "note-1", anchor: document.body });
});

afterEach(() => {
    document.body.replaceChildren();
});

describe("schedule picker keyboard", () => {
    it("opens with the month box focused", () => {
        expect(document.activeElement).toBe(boxes()[MONTH]);
    });

    it("moves to the next box once a box is full", () => {
        const month = boxes()[MONTH];
        month.focus();
        month.value = "1";
        month.dispatchEvent(new Event("input", { bubbles: true }));
        expect(document.activeElement).toBe(month);

        month.value = "12";
        month.dispatchEvent(new Event("input", { bubbles: true }));
        expect(document.activeElement).toBe(boxes()[DAY]);
    });

    it("drops characters that are not digits", () => {
        put(MONTH, "1a2");
        expect(boxes()[MONTH].value).toBe("12");
    });

    it("steps a box with the arrow keys and stops at the bounds", () => {
        put(HOUR, "12");
        press(HOUR, "ArrowUp");
        expect(boxes()[HOUR].value).toBe("12");

        press(HOUR, "ArrowDown");
        expect(boxes()[HOUR].value).toBe("11");
    });

    it("never steps the hour below one", () => {
        put(HOUR, "01");
        press(HOUR, "ArrowDown");
        expect(boxes()[HOUR].value).toBe("01");
    });

    it("saves the typed stamp when Enter is pressed", () => {
        put(MONTH, "03");
        put(DAY, "07");
        put(YEAR, "2027");
        put(HOUR, "08");
        put(MINUTE, "15");
        chooserMeridiem("AM");

        press(MINUTE, "Enter");

        expect(scheduleMock).toHaveBeenCalledWith("note-1", "x", new Date(2027, 2, 7, 8, 15).getTime());
    });

    it("closes on Escape", () => {
        press(MINUTE, "Escape");
        expect(document.querySelector(".plan-popover")).toBeNull();
    });
});
