import "./plan.css";
import { Events } from "@wailsio/runtime";
import * as PlannerService from "../bindings/quire/plannerservice.js";
import type { BacklogItem, PlanState, QueueItem } from "../bindings/quire/models.js";
import { PLATFORMS, platformAbbr, platformLabel } from "./platforms.js";

export interface PlannerHost {
    showToast(message: string): void;
    openNote(id: string): Promise<void>;
    openPublishForSlot(noteID: string, slotID: string): Promise<void>;
    flushSave(): Promise<void>;
    confirm(title: string, message: string, action?: string): Promise<boolean>;
    setScreen(screen: "home" | "notes" | "queue" | "calendar"): void;
    refreshStatuses(): Promise<void>;
}

type QueueTab = "upcoming" | "history";
type HistoryFilter = "all" | "done" | "skipped";

interface DayFilter {
    start: number;
    end: number;
    label: string;
}

interface DayGroup {
    key: string;
    label: string;
    items: QueueItem[];
}

let host: PlannerHost | null = null;
let currentScreen: "queue" | "calendar" = "queue";
let queueTab: QueueTab = "upcoming";
let historyFilter: HistoryFilter = "all";
let dayFilter: DayFilter | null = null;
let monthCursor = startOfMonth(new Date());
let selectedDay = dayKey(Date.now());
let calendarItems: QueueItem[] = [];
let dueItems = new Map<string, QueueItem>();
let popover: HTMLElement | null = null;
let popoverReturnFocus: HTMLElement | null = null;

const WEEKDAYS = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];

function requireHost(): PlannerHost {
    if (!host) throw new Error("planner is not initialised");
    return host;
}

function root(): HTMLElement {
    return document.getElementById("planner-view") as HTMLElement;
}

function banner(): HTMLElement {
    return document.getElementById("due-banner") as HTMLElement;
}

export function initPlanner(plannerHost: PlannerHost): void {
    host = plannerHost;
    Events.On("planner:due", (event) => {
        const item = event.data as QueueItem | null;
        if (!item || !item.noteId || !item.slotId) return;
        dueItems.set(`${item.noteId}:${item.slotId}`, item);
        renderDueBanner();
    });
    renderDueBanner();
}

export async function renderPlannerScreen(screen: "queue" | "calendar"): Promise<void> {
    currentScreen = screen;
    if (screen === "queue") {
        await renderQueue();
    } else {
        await renderCalendar();
    }
}

// ---------------------------------------------------------------------------
// Small DOM helpers
// ---------------------------------------------------------------------------

function el<K extends keyof HTMLElementTagNameMap>(tag: K, className?: string): HTMLElementTagNameMap[K] {
    const node = document.createElement(tag);
    if (className) node.className = className;
    return node;
}

function text(tag: keyof HTMLElementTagNameMap, className: string, value: string): HTMLElement {
    const node = el(tag, className);
    node.textContent = value;
    return node;
}

function button(className: string, label: string, onClick: () => void): HTMLButtonElement {
    const node = el("button", className);
    node.type = "button";
    node.textContent = label;
    node.addEventListener("click", (event) => {
        event.stopPropagation();
        onClick();
    });
    return node;
}

function errorMessage(err: unknown): string {
    return err instanceof Error ? err.message : String(err);
}

function pad(value: number): string {
    return String(value).padStart(2, "0");
}

function startOfMonth(date: Date): Date {
    return new Date(date.getFullYear(), date.getMonth(), 1);
}

function dayKey(ms: number): string {
    const date = new Date(ms);
    return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}

function startOfLocalDay(ms: number): number {
    const date = new Date(ms);
    return new Date(date.getFullYear(), date.getMonth(), date.getDate()).getTime();
}

function nextDayStart(ms: number): number {
    const date = new Date(startOfLocalDay(ms));
    date.setDate(date.getDate() + 1);
    return date.getTime();
}

function timeLabel(ms: number): string {
    return new Date(ms).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

function dayHeading(ms: number): string {
    const key = dayKey(ms);
    const now = new Date();
    const today = dayKey(now.getTime());
    const tomorrow = dayKey(new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1).getTime());
    if (key === today) return "Today";
    if (key === tomorrow) return "Tomorrow";
    return new Date(ms).toLocaleDateString(undefined, {
        weekday: "short",
        day: "numeric",
        month: "short",
    });
}

function monthLabel(month: Date): string {
    return month.toLocaleDateString(undefined, { month: "long", year: "numeric" });
}

function groupByDay(
    items: QueueItem[],
    timeOf: (item: QueueItem) => number,
    direction: "asc" | "desc",
): DayGroup[] {
    const sorted = [...items].sort((a, b) => {
        const diff = timeOf(a) - timeOf(b);
        return direction === "asc" ? diff : -diff;
    });
    const groups: DayGroup[] = [];
    const index = new Map<string, DayGroup>();
    for (const item of sorted) {
        const key = dayKey(timeOf(item));
        let group = index.get(key);
        if (!group) {
            group = { key, label: dayHeading(timeOf(item)), items: [] };
            index.set(key, group);
            groups.push(group);
        }
        group.items.push(item);
    }
    return groups;
}

// ---------------------------------------------------------------------------
// Shared shell
// ---------------------------------------------------------------------------

function shell(title: string): { toolbar: HTMLElement; scroll: HTMLElement } {
    const view = root();
    view.hidden = false;
    view.replaceChildren();

    const toolbar = el("div", "planner-toolbar");
    // The desktop sidebar is a drawer on mobile, so the planner screens need
    // their own way back to the note list and the screen switcher.
    const menu = el("button", "icon-btn mobile-only");
    menu.type = "button";
    menu.textContent = "☰";
    menu.dataset.sidebarToggle = "";
    menu.title = "Notes list";
    menu.setAttribute("aria-label", "Open notes list");
    menu.setAttribute("aria-controls", "sidebar");
    menu.setAttribute("aria-expanded", "false");
    toolbar.append(menu);
    toolbar.append(text("h1", "planner-title", title));
    toolbar.append(el("div", "spacer"));
    view.append(toolbar);

    const scroll = el("div", "planner-scroll");
    view.append(scroll);
    return { toolbar, scroll };
}

async function refresh(): Promise<void> {
    try {
        await requireHost().refreshStatuses();
    } catch {
        /* the sidebar falls back to an ungrouped list on its own */
    }
    if (currentScreen === "queue") {
        await renderQueue();
    } else {
        await renderCalendar();
    }
}

async function mutate(action: () => Promise<unknown>, success: string): Promise<void> {
    try {
        await action();
        requireHost().showToast(success);
        await refresh();
    } catch (err) {
        requireHost().showToast(`${success} failed: ${errorMessage(err)}`);
    }
}

// ---------------------------------------------------------------------------
// Queue and history
// ---------------------------------------------------------------------------

async function renderQueue(): Promise<void> {
    const { toolbar, scroll } = shell("Queue");

    const tabs = el("div", "segmented");
    tabs.setAttribute("role", "tablist");
    for (const tab of ["upcoming", "history"] as QueueTab[]) {
        const label = tab === "upcoming" ? "Upcoming" : "History";
        const node = el("button");
        node.type = "button";
        node.textContent = label;
        node.classList.toggle("is-active", queueTab === tab);
        node.setAttribute("aria-selected", String(queueTab === tab));
        node.addEventListener("click", () => {
            if (queueTab === tab) return;
            queueTab = tab;
            dayFilter = null;
            void renderQueue();
        });
        tabs.append(node);
    }
    toolbar.append(tabs);

    if (queueTab === "upcoming") {
        await renderUpcoming(scroll);
    } else {
        await renderHistory(scroll);
    }
}

async function renderUpcoming(scroll: HTMLElement): Promise<void> {
    try {
        const from = dayFilter?.start ?? 0;
        const to = dayFilter?.end ?? 0;
        const [items, backlog] = await Promise.all([
            PlannerService.Queue(from, to),
            PlannerService.Backlog(20),
        ]);
        const queue = items ?? [];
        const backlogItems = backlog ?? [];

        if (dayFilter) {
            const filters = el("div", "queue-filters");
            filters.append(text("span", "queue-status", dayFilter.label));
            filters.append(button("action-chip", "Clear day filter", () => {
                dayFilter = null;
                void renderQueue();
            }));
            scroll.append(filters);
        }

        const overdue = queue.filter((item) => item.status === "overdue");
        const upcoming = queue.filter((item) => item.status !== "overdue");

        if (queue.length === 0) {
            scroll.append(
                text(
                    "p",
                    "planner-empty",
                    dayFilter
                        ? "Nothing scheduled for this day."
                        : "Nothing scheduled yet. Open a note and use Schedule.",
                ),
            );
        }

        if (overdue.length > 0) {
            const group = el("section", "queue-day");
            group.dataset.overdue = "true";
            group.append(text("h2", "queue-day-head", "Overdue"));
            group.append(queueList(overdue, false));
            scroll.append(group);
        }

        for (const group of groupByDay(upcoming, (item) => item.scheduledAt, "asc")) {
            const section = el("section", "queue-day");
            section.append(text("h2", "queue-day-head", group.label));
            section.append(queueList(group.items, false));
            scroll.append(section);
        }

        scroll.append(backlogSection(backlogItems));
    } catch (err) {
        scroll.append(text("p", "planner-empty", `Could not load the queue: ${errorMessage(err)}`));
    }
}

function backlogSection(items: BacklogItem[]): HTMLElement {
    const section = el("section", "backlog-section");
    section.append(text("h2", "queue-day-head", "Unscheduled"));
    if (items.length === 0) {
        section.append(text("p", "planner-empty", "Every note has a scheduled slot or has been published."));
        return section;
    }
    const list = el("ul", "backlog-list");
    for (const item of items) {
        const row = el("li");
        const node = el("button", "backlog-row");
        node.type = "button";
        node.append(text("span", "queue-title", item.title || "Untitled note"));
        node.append(text("span", "queue-status", statusLabel(item.status)));
        node.addEventListener("click", () => void requireHost().openNote(item.noteId));
        row.append(node);
        list.append(row);
    }
    section.append(list);
    return section;
}

async function renderHistory(scroll: HTMLElement): Promise<void> {
    try {
        const items = (await PlannerService.History(0, 0)) ?? [];
        const visible = items.filter((item) => {
            if (historyFilter === "all") return true;
            return item.status === historyFilter;
        });

        const filters = el("div", "queue-filters");
        for (const value of ["all", "done", "skipped"] as HistoryFilter[]) {
            const label = value === "all" ? "All" : value === "done" ? "Done" : "Skipped";
            const node = button("filter-chip", label, () => {
                historyFilter = value;
                void renderQueue();
            });
            node.classList.toggle("is-active", historyFilter === value);
            filters.append(node);
        }
        scroll.append(filters);

        if (visible.length === 0) {
            scroll.append(
                text(
                    "p",
                    "planner-empty",
                    items.length === 0
                        ? "No completed posts yet. Mark a queue item as posted or skipped."
                        : "No history matches this filter.",
                ),
            );
            return;
        }

        const groups = groupByDay(visible, (item) => item.completedAt ?? item.scheduledAt, "desc");
        for (const group of groups) {
            const section = el("section", "queue-day");
            section.append(text("h2", "queue-day-head", group.label));
            section.append(queueList(group.items, true));
            scroll.append(section);
        }
    } catch (err) {
        scroll.append(text("p", "planner-empty", `Could not load history: ${errorMessage(err)}`));
    }
}

function statusLabel(status: string): string {
    switch (status) {
        case "overdue":
            return "Overdue";
        case "scheduled":
            return "Scheduled";
        case "ready":
            return "Ready";
        case "drafting":
        case "draft":
            return "Drafting";
        case "idea":
            return "Idea";
        case "posted":
            return "Posted";
        case "published":
            return "Published";
        case "done":
            return "Done";
        case "skipped":
            return "Skipped";
        default:
            return status;
    }
}

function queueList(items: QueueItem[], history: boolean): HTMLElement {
    const list = el("div", "queue-list");
    for (const item of items) {
        list.append(queueRow(item, history));
    }
    return list;
}

function queueRow(item: QueueItem, history: boolean): HTMLElement {
    const row = el("div", "queue-row");
    row.dataset.noteId = item.noteId;
    row.dataset.slotId = item.slotId;
    row.dataset.history = String(history);
    const time = item.completedAt && history ? item.completedAt : item.scheduledAt;
    row.append(text("span", "queue-time", timeLabel(time)));

    const badge = text("span", "queue-badge", platformAbbr(item.platform));
    badge.title = platformLabel(item.platform);
    row.append(badge);

    const main = el("div", "queue-main");
    main.append(text("span", "queue-title", item.title || "Untitled note"));
    if (item.preview) main.append(text("span", "queue-preview", item.preview));
    row.append(main);

    if (history) {
        const status = text("span", "queue-status", statusLabel(item.status));
        status.dataset.status = item.status;
        row.append(status);
    }

    if (item.stale) {
        const stale = el("span", "queue-stale");
        stale.title = "Note changed since this draft";
        row.append(stale);
    }

    const open = () => void requireHost().openNote(item.noteId);
    row.addEventListener("click", open);
    row.tabIndex = 0;
    row.setAttribute("role", "button");
    row.addEventListener("keydown", (event) => {
        if (event.key === "Enter" || event.key === " ") {
            event.preventDefault();
            open();
        }
    });

    if (!history) {
        row.append(queueMenu(item));
    }
    return row;
}

function queueMenu(item: QueueItem): HTMLElement {
    const wrap = el("div");
    const trigger = el("button", "queue-menu-btn");
    trigger.type = "button";
    trigger.textContent = "…";
    trigger.setAttribute("aria-label", `Actions for ${item.title}`);

    const menu = el("div", "queue-menu");
    menu.hidden = true;

    menu.append(
        button("", "Reschedule", () => {
            menu.hidden = true;
            void openSchedulePopover({
                noteId: item.noteId,
                slotId: item.slotId,
                platform: item.platform,
                at: item.scheduledAt,
                anchor: trigger,
                onSaved: () => void refresh(),
            });
        }),
        button("", "Mark posted", () => {
            menu.hidden = true;
            void mutate(() => PlannerService.MarkSlot(item.noteId, item.slotId, "done"), "Marked as posted");
        }),
        button("", "Skip", () => {
            menu.hidden = true;
            void mutate(() => PlannerService.MarkSlot(item.noteId, item.slotId, "skipped"), "Skipped");
        }),
        button("danger-text", "Unschedule", () => {
            menu.hidden = true;
            void mutate(() => PlannerService.Unschedule(item.noteId, item.slotId), "Unscheduled");
        }),
    );

    trigger.addEventListener("click", (event) => {
        event.stopPropagation();
        const wasOpen = !menu.hidden;
        closeQueueMenus();
        menu.hidden = wasOpen;
    });

    wrap.append(trigger, menu);
    return wrap;
}

function closeQueueMenus(): void {
    for (const menu of document.querySelectorAll<HTMLElement>(".queue-menu")) {
        menu.hidden = true;
    }
}

document.addEventListener("click", () => closeQueueMenus());

// j/k move between queue rows, Enter opens, m marks posted. Shortcuts stay out
// of the way while typing, while a popover/modal is open, or in the row menu.
document.addEventListener("keydown", (event) => {
    if (event.defaultPrevented) return;
    if (currentScreen !== "queue" || root().hidden) return;
    if (document.querySelector(".plan-popover")) return;
    if (document.querySelector(".modal-backdrop:not([hidden])")) return;
    const target = event.target as HTMLElement | null;
    if (
        target?.closest(
            "input, textarea, select, button, .queue-menu, [contenteditable='true']",
        )
    ) {
        return;
    }

    const rows = Array.from(root().querySelectorAll<HTMLElement>(".queue-row"));
    if (rows.length === 0) return;
    const active = rows.indexOf(document.activeElement as HTMLElement);
    let index = active;
    switch (event.key) {
        case "j":
            index = active < 0 ? 0 : Math.min(active + 1, rows.length - 1);
            break;
        case "k":
            index = active < 0 ? 0 : Math.max(active - 1, 0);
            break;
        case "Enter":
            if (active < 0) return;
            event.preventDefault();
            rows[active].click();
            return;
        case "m": {
            if (active < 0) return;
            const row = rows[active];
            if (row.dataset.history === "true") return;
            const noteId = row.dataset.noteId;
            const slotId = row.dataset.slotId;
            if (!noteId || !slotId) return;
            event.preventDefault();
            void mutate(
                () => PlannerService.MarkSlot(noteId, slotId, "done"),
                "Marked as posted",
            );
            return;
        }
        default:
            return;
    }
    event.preventDefault();
    rows[index]?.focus();
    rows[index]?.scrollIntoView({ block: "nearest" });
});

// ---------------------------------------------------------------------------
// Calendar
// ---------------------------------------------------------------------------

async function renderCalendar(): Promise<void> {
    const { toolbar, scroll } = shell("Calendar");

    const prev = button("icon-btn", "‹", () => {
        monthCursor = new Date(monthCursor.getFullYear(), monthCursor.getMonth() - 1, 1);
        void renderCalendar();
    });
    const next = button("icon-btn", "›", () => {
        monthCursor = new Date(monthCursor.getFullYear(), monthCursor.getMonth() + 1, 1);
        void renderCalendar();
    });
    const today = button("action-chip", "Today", () => {
        const now = new Date();
        monthCursor = startOfMonth(now);
        selectedDay = dayKey(now.getTime());
        void renderCalendar();
    });
    toolbar.append(prev, next, today);

    const calendar = el("div", "calendar");
    const head = el("div", "calendar-head");
    head.append(text("span", "calendar-month", monthLabel(monthCursor)));
    calendar.append(head);

    const grid = el("div", "calendar-grid");
    grid.setAttribute("role", "grid");
    grid.tabIndex = 0;
    for (const day of WEEKDAYS) {
        const cell = text("div", "calendar-weekday", day);
        cell.setAttribute("role", "columnheader");
        grid.append(cell);
    }

    const first = new Date(monthCursor.getFullYear(), monthCursor.getMonth(), 1);
    const offset = (first.getDay() + 6) % 7;
    const gridStart = new Date(monthCursor.getFullYear(), monthCursor.getMonth(), 1 - offset);
    const gridEnd = new Date(gridStart.getFullYear(), gridStart.getMonth(), gridStart.getDate() + 42);

    try {
        calendarItems =
            (await PlannerService.Queue(gridStart.getTime(), gridEnd.getTime())) ?? [];
    } catch (err) {
        scroll.append(text("p", "planner-empty", `Could not load the calendar: ${errorMessage(err)}`));
        return;
    }

    const byDay = new Map<string, QueueItem[]>();
    for (const item of calendarItems) {
        const key = dayKey(item.scheduledAt);
        const list = byDay.get(key) ?? [];
        list.push(item);
        byDay.set(key, list);
    }
    for (const list of byDay.values()) {
        list.sort((a, b) => a.scheduledAt - b.scheduledAt);
    }

    const todayKey = dayKey(Date.now());
    for (let index = 0; index < 42; index++) {
        const date = new Date(gridStart.getFullYear(), gridStart.getMonth(), gridStart.getDate() + index);
        const key = dayKey(date.getTime());
        const cell = el("div", "calendar-cell");
        cell.setAttribute("role", "gridcell");
        cell.setAttribute("aria-selected", String(key === selectedDay));
        cell.classList.toggle("is-outside", date.getMonth() !== monthCursor.getMonth());
        cell.classList.toggle("is-today", key === todayKey);
        cell.classList.toggle("is-selected", key === selectedDay);
        cell.dataset.date = key;
        cell.addEventListener("click", (event) => {
            if ((event.target as HTMLElement).closest("button, a")) return;
            openDay(date.getTime());
        });

        const dayNumber = el("button", "calendar-daynum");
        dayNumber.type = "button";
        dayNumber.textContent = String(date.getDate());
        dayNumber.addEventListener("click", () => openDay(date.getTime()));
        cell.append(dayNumber);

        const items = byDay.get(key) ?? [];
        for (const item of items.slice(0, 2)) {
            const chip = el("button", "calendar-chip");
            chip.type = "button";
            chip.textContent = `${timeLabel(item.scheduledAt)} ${platformAbbr(item.platform)}`;
            chip.title = `${platformLabel(item.platform)} · ${item.title}`;
            chip.setAttribute(
                "aria-label",
                `Post "${item.title}" to ${platformLabel(item.platform)} at ${timeLabel(item.scheduledAt)}, ${new Date(item.scheduledAt).toLocaleDateString()}`,
            );
            chip.addEventListener("click", () => void requireHost().openNote(item.noteId));
            cell.append(chip);
        }
        if (items.length > 2) {
            const more = button("calendar-more", `+${items.length - 2} more`, () => openDay(date.getTime()));
            cell.append(more);
        }

        grid.append(cell);
    }

    grid.addEventListener("keydown", (event) => {
        const current = new Date(`${selectedDay}T00:00`);
        let handled = true;
        switch (event.key) {
            case "ArrowLeft":
                current.setDate(current.getDate() - 1);
                break;
            case "ArrowRight":
                current.setDate(current.getDate() + 1);
                break;
            case "ArrowUp":
                current.setDate(current.getDate() - 7);
                break;
            case "ArrowDown":
                current.setDate(current.getDate() + 7);
                break;
            case "PageUp":
                current.setMonth(current.getMonth() - 1);
                break;
            case "PageDown":
                current.setMonth(current.getMonth() + 1);
                break;
            case "Enter":
                openDay(current.getTime());
                handled = false;
                break;
            case "t":
                selectedDay = todayKey;
                monthCursor = startOfMonth(new Date());
                break;
            default:
                handled = false;
        }
        if (!handled) return;
        event.preventDefault();
        selectedDay = dayKey(current.getTime());
        monthCursor = startOfMonth(current);
        void renderCalendar();
    });

    calendar.append(grid);
    scroll.append(calendar);
}

function openDay(ms: number): void {
    const start = startOfLocalDay(ms);
    const end = nextDayStart(ms);
    dayFilter = { start, end, label: dayHeading(ms) };
    queueTab = "upcoming";
    currentScreen = "queue";
    // setScreen re-renders the planner screen, so no extra renderQueue here.
    requireHost().setScreen("queue");
}

// ---------------------------------------------------------------------------
// Schedule popover
// ---------------------------------------------------------------------------

interface SchedulePopoverOptions {
    noteId: string;
    slotId?: string;
    platform?: string;
    at?: number;
    anchor: HTMLElement;
    onSaved?: () => void;
}

function nextHalfHour(): number {
    const date = new Date();
    date.setSeconds(0, 0);
    const minutes = date.getMinutes();
    date.setMinutes(minutes + (30 - (minutes % 30)));
    return date.getTime();
}


function scheduledSlot(plan: PlanState | null, platform: string) {
    if (!plan) return null;
    return (
        (plan.slots ?? []).find(
            (slot) => slot.status === "scheduled" && slot.platform === platform && slot.scheduledAt > 0,
        ) ?? null
    );
}

export async function openSchedulePopover(options: SchedulePopoverOptions): Promise<void> {
    closePopover();

    let plan: PlanState | null = null;
    if (!options.slotId) {
        try {
            plan = await PlannerService.Plan(options.noteId);
        } catch {
            plan = null;
        }
    }

    let platform = options.platform ?? "x";
    const fixedPlatform = Boolean(options.slotId);
    const existing = options.slotId ? null : scheduledSlot(plan, platform);
    const initialAt = options.at ?? existing?.scheduledAt ?? nextHalfHour();

    const panel = el("div", "plan-popover");
    panel.setAttribute("role", "dialog");
    panel.setAttribute("aria-label", options.slotId ? "Reschedule post" : "Schedule post");
    panel.append(text("h3", "", options.slotId ? "Reschedule post" : "Schedule post"));

    if (!fixedPlatform) {
        const targets = el("div", "segmented");
        const platformButtons = new Map<string, HTMLButtonElement>();
        for (const info of PLATFORMS) {
            const node = el("button");
            node.type = "button";
            node.textContent = info.label;
            node.dataset.platform = info.id;
            node.classList.toggle("is-active", info.id === platform);
            platformButtons.set(info.id, node);
        }
        targets.append(...platformButtons.values());
        panel.append(targets);
    } else {
        panel.append(text("p", "plan-field", platformLabel(platform)));
    }

    // Native date and time inputs paint their active segment in the system
    // highlight colour, which no stylesheet can reach. These number boxes keep
    // the popover entirely inside the palette.
    const boxes: HTMLInputElement[] = [];
    const makeBox = (className: string, length: number, label: string, max: number): HTMLInputElement => {
        const input = document.createElement("input");
        input.type = "text";
        input.className = `plan-num ${className}`;
        input.inputMode = "numeric";
        input.autocomplete = "off";
        input.maxLength = length;
        input.setAttribute("aria-label", label);
        input.addEventListener("focus", () => input.select());
        input.addEventListener("input", () => {
            input.value = input.value.replace(/\D/g, "").slice(0, length);
            const index = boxes.indexOf(input);
            if (input.value.length === length && index >= 0 && index < boxes.length - 1) {
                boxes[index + 1].focus();
            }
        });
        input.addEventListener("keydown", (event) => {
            const step = event.key === "ArrowUp" ? 1 : event.key === "ArrowDown" ? -1 : 0;
            if (!step) return;
            event.preventDefault();
            const next = Math.min(max, Math.max(0, Number(input.value || 0) + step));
            input.value = String(next).padStart(length, "0");
        });
        boxes.push(input);
        return input;
    };

    let meridiem: "am" | "pm" = "pm";
    const meridiemButtons = new Map<"am" | "pm", HTMLButtonElement>();
    const setMeridiem = (value: "am" | "pm") => {
        meridiem = value;
        for (const [key, node] of meridiemButtons) node.classList.toggle("is-active", key === value);
    };

    const dateField = el("label", "plan-field");
    dateField.append(text("span", "", "Date"));
    const dateRow = el("div", "plan-date");
    const monthBox = makeBox("plan-month", 2, "Month", 12);
    const dayBox = makeBox("plan-day", 2, "Day", 31);
    const yearBox = makeBox("plan-year", 4, "Year", 2999);
    dateRow.append(monthBox, text("span", "plan-sep", "/"), dayBox, text("span", "plan-sep", "/"), yearBox);
    dateField.append(dateRow);
    panel.append(dateField);

    const timeField = el("label", "plan-field");
    timeField.append(text("span", "", "Time"));
    const timeRow = el("div", "plan-time");
    const hourBox = makeBox("plan-hour", 2, "Hour", 12);
    const minuteBox = makeBox("plan-minute", 2, "Minute", 59);
    const meridiemToggle = el("div", "segmented segmented-sm plan-ampm");
    for (const value of ["am", "pm"] as const) {
        const node = el("button");
        node.type = "button";
        node.textContent = value.toUpperCase();
        node.addEventListener("click", () => setMeridiem(value));
        meridiemButtons.set(value, node);
        meridiemToggle.append(node);
    }
    timeRow.append(hourBox, text("span", "plan-sep", ":"), minuteBox, meridiemToggle);
    timeField.append(timeRow);
    panel.append(timeField);

    type Picked = { local: Date; hour24: number; minute: number; day: number };

    const writeStamp = (at: number) => {
        const date = new Date(at);
        const hours = date.getHours();
        monthBox.value = String(date.getMonth() + 1).padStart(2, "0");
        dayBox.value = String(date.getDate()).padStart(2, "0");
        yearBox.value = String(date.getFullYear());
        hourBox.value = String(hours % 12 === 0 ? 12 : hours % 12).padStart(2, "0");
        minuteBox.value = String(date.getMinutes()).padStart(2, "0");
        setMeridiem(hours < 12 ? "am" : "pm");
    };

    const readStamp = (): Picked | null => {
        const fields = [monthBox, dayBox, yearBox, hourBox, minuteBox];
        if (fields.some((box) => box.value === "")) return null;
        const [month, day, year, hour12, minute] = fields.map((box) => Number(box.value));
        if (
            ![month, day, year, hour12, minute].every(Number.isInteger) ||
            month < 1 || month > 12 ||
            day < 1 || day > 31 ||
            year < 1970 || year > 2999 ||
            hour12 < 1 || hour12 > 12 ||
            minute < 0 || minute > 59
        ) {
            return null;
        }
        const hour24 = (hour12 % 12) + (meridiem === "pm" ? 12 : 0);
        return { local: new Date(year, month - 1, day, hour24, minute, 0, 0), hour24, minute, day };
    };

    writeStamp(initialAt);

    const error = text("p", "plan-error", "");
    error.hidden = true;
    panel.append(error);

    const actions = el("div", "plan-actions");
    const primary = el("button", "btn primary");
    primary.type = "button";
    const cancel = button("btn", "Cancel", () => closePopover());
    actions.append(cancel, primary);
    panel.append(actions);

    let confirmedGap = false;

    const updatePrimary = () => {
        if (fixedPlatform) {
            primary.textContent = "Reschedule";
        } else {
            primary.textContent = scheduledSlot(plan, platform) ? "Update" : "Schedule";
        }
    };
    updatePrimary();

    for (const node of panel.querySelectorAll<HTMLButtonElement>(".segmented button")) {
        node.addEventListener("click", () => {
            platform = node.dataset.platform ?? platform;
            for (const other of panel.querySelectorAll<HTMLButtonElement>(".segmented button")) {
                other.classList.toggle("is-active", other === node);
            }
            const slot = scheduledSlot(plan, platform);
            if (slot) writeStamp(slot.scheduledAt);
            updatePrimary();
            confirmedGap = false;
            error.hidden = true;
        });
    }

    primary.addEventListener("click", () => {
        error.hidden = true;
        const picked = readStamp();
        if (!picked) {
            error.textContent = "Choose a date and time.";
            error.hidden = false;
            return;
        }
        const local = picked.local;
        if (Number.isNaN(local.getTime()) || local.getDate() !== picked.day) {
            error.textContent = "That date and time is not valid.";
            error.hidden = false;
            return;
        }
        if (
            !confirmedGap &&
            (local.getHours() !== picked.hour24 || local.getMinutes() !== picked.minute)
        ) {
            confirmedGap = true;
            error.textContent = `That wall-clock time does not exist in this timezone; it will be saved as ${timeLabel(local.getTime())}. Press again to confirm.`;
            error.hidden = false;
            return;
        }

        const at = local.getTime();
        primary.disabled = true;
        const action = options.slotId
            ? PlannerService.Reschedule(options.noteId, options.slotId, at)
            : PlannerService.Schedule(options.noteId, platform, at);
        action
            .then(() => {
                closePopover();
                options.onSaved?.();
            })
            .catch((err) => {
                error.textContent = errorMessage(err);
                error.hidden = false;
                primary.disabled = false;
            });
    });

    popoverReturnFocus = document.activeElement as HTMLElement | null;
    document.body.append(panel);
    popover = panel;
    positionPopover(panel, options.anchor);
    monthBox.focus();

    panel.addEventListener("keydown", (event) => {
        if (event.key === "Escape") {
            event.preventDefault();
            closePopover();
        } else if (event.key === "Enter" && event.target !== cancel) {
            event.preventDefault();
            primary.click();
        }
    });

    setTimeout(() => {
        document.addEventListener("mousedown", onOutsidePopover, { once: true });
    });
}

function onOutsidePopover(event: MouseEvent): void {
    if (!popover) return;
    if (!popover.contains(event.target as Node)) {
        closePopover();
    } else {
        document.addEventListener("mousedown", onOutsidePopover, { once: true });
    }
}

function positionPopover(panel: HTMLElement, anchor: HTMLElement): void {
    const rect = anchor.getBoundingClientRect();
    const width = 300;
    const left = Math.min(Math.max(8, rect.left), window.innerWidth - width - 8);
    const height = panel.offsetHeight || 250;
    let top = rect.bottom + 6;
    if (top + height > window.innerHeight - 8) {
        top = Math.max(8, rect.top - height - 6);
    }
    panel.style.left = `${left}px`;
    panel.style.top = `${top}px`;
}

function closePopover(): void {
    if (!popover) return;
    popover.remove();
    popover = null;
    document.removeEventListener("mousedown", onOutsidePopover);
    popoverReturnFocus?.focus();
    popoverReturnFocus = null;
}

export function openSchedulerForNote(noteId: string, anchor: HTMLElement): void {
    void openSchedulePopover({
        noteId,
        anchor,
        onSaved: () => {
            void requireHost().refreshStatuses();
            if (currentScreen === "queue" && !root().hidden) void renderQueue();
        },
    });
}

// ---------------------------------------------------------------------------
// Reminder banner
// ---------------------------------------------------------------------------

function renderDueBanner(): void {
    const node = banner();
    node.replaceChildren();
    if (dueItems.size === 0) {
        node.hidden = true;
        return;
    }
    node.hidden = false;

    const head = el("div", "due-banner-head");
    head.append(text("span", "", "Time to post"));
    head.append(el("span", "spacer"));
    head.append(
        button("", "Dismiss all", () => {
            dueItems = new Map();
            renderDueBanner();
        }),
    );
    node.append(head);

    for (const [key, item] of dueItems) {
        const row = el("div", "due-row");
        const top = el("div", "due-row-top");
        top.append(text("span", "", `${timeLabel(item.scheduledAt)} · ${platformLabel(item.platform)}`));
        row.append(top);
        row.append(text("span", "due-title", item.title || "Untitled note"));

        const actions = el("div", "due-actions");
        actions.append(
            button("btn", "Copy", () => void copySlot(item)),
            button("btn primary", "Open", () => void requireHost().openPublishForSlot(item.noteId, item.slotId)),
            button("action-chip", "Dismiss", () => {
                dueItems.delete(key);
                renderDueBanner();
            }),
        );
        row.append(actions);
        node.append(row);
    }
}

async function copySlot(item: QueueItem): Promise<void> {
    try {
        const text = await PlannerService.SlotText(item.noteId, item.slotId);
        const value =
            text.text ||
            (text.parts && text.parts.length > 0 ? text.parts.join("\n\n") : "") ||
            text.body ||
            text.title;
        if (!value) {
            requireHost().showToast("This slot has no text to copy yet");
            return;
        }
        await writeClipboard(value);
        requireHost().showToast("Post copied to clipboard");
    } catch (err) {
        requireHost().showToast(`Could not copy post: ${errorMessage(err)}`);
    }
}

async function writeClipboard(value: string): Promise<void> {
    try {
        await navigator.clipboard.writeText(value);
    } catch {
        const temp = document.createElement("textarea");
        temp.value = value;
        document.body.append(temp);
        temp.select();
        document.execCommand("copy");
        temp.remove();
    }
}
