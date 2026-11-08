import "./home.css";
import * as NotesService from "../bindings/post-planner/notesservice.js";
import type { Category, NoteSummary } from "../bindings/post-planner/models.js";

// The home screen is the front door: it says where this note belongs and what
// each place is for. It is built from the same data the sidebar uses, so a
// change made here is visible in the note list straight away.

export interface HomeData {
    categories: Category[];
    notes: NoteSummary[];
}

export interface HomeHost {
    showToast(message: string): void;
    data(): HomeData;
    refresh(): Promise<void>;
    newNote(categoryID?: string): Promise<void>;
    openTodayJournal(): Promise<void>;
    openNote(id: string): Promise<void>;
    filterNotes(categoryID: string | null): Promise<void>;
    setScreen(screen: "notes" | "queue" | "calendar"): void;
    confirm(title: string, message: string, action?: string): Promise<boolean>;
}

let host: HomeHost | null = null;
let editingID: string | null = null;
let dialogReturnFocus: HTMLElement | null = null;

function requireHost(): HomeHost {
    if (!host) throw new Error("home screen is not initialised");
    return host;
}

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
    node.addEventListener("click", onClick);
    return node;
}

function errorMessage(err: unknown): string {
    return err instanceof Error ? err.message : String(err);
}

function pad(value: number): string {
    return String(value).padStart(2, "0");
}

// Local calendar day, matching the backend's journal titles.
function todayKey(): string {
    const now = new Date();
    return `${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}`;
}

function todayLabel(): string {
    return new Date().toLocaleDateString(undefined, {
        weekday: "long",
        day: "numeric",
        month: "long",
    });
}

function greeting(): string {
    const hour = new Date().getHours();
    const part = hour < 5 ? "Still up" : hour < 12 ? "Good morning" : hour < 18 ? "Good afternoon" : "Good evening";
    return `${part} · ${todayLabel()}`;
}

function journalCategory(categories: Category[]): Category | undefined {
    return categories.find((category) => category.kind === "journal");
}

function notesIn(categoryID: string, notes: NoteSummary[]): NoteSummary[] {
    return notes.filter((note) => (note.categoryId || "") === categoryID);
}

function todayEntry(category: Category | undefined, notes: NoteSummary[]): NoteSummary | undefined {
    if (!category) return undefined;
    const key = todayKey();
    return notes.find(
        (note) => (note.categoryId || "") === category.id && note.title.trim() === key,
    );
}

// ---------------------------------------------------------------------------
// Render
// ---------------------------------------------------------------------------

export function initHome(homeHost: HomeHost): void {
    host = homeHost;
    bindCategoryDialog();
}

export async function renderHomeScreen(): Promise<void> {
    const view = document.getElementById("home-view") as HTMLElement | null;
    if (!view) return;

    const current = requireHost();
    const { categories, notes } = current.data();

    view.replaceChildren();
    const scroll = el("div", "home-scroll");
    const inner = el("div", "home-inner");
    scroll.append(inner);

    inner.append(homeTopbar(), hero(current, categories, notes));

    const journal = journalCategory(categories);
    inner.append(categoriesSection(current, categories, notes, journal));
    inner.append(planSection(current, notes));
    inner.append(recentSection(current, notes));

    view.append(scroll);
}

function homeTopbar(): HTMLElement {
    const bar = el("div", "home-topbar");
    const menu = el("button", "icon-btn mobile-only");
    menu.type = "button";
    menu.textContent = "☰";
    menu.dataset.sidebarToggle = "";
    menu.title = "Notes list";
    menu.setAttribute("aria-label", "Open notes list");
    menu.setAttribute("aria-controls", "sidebar");
    menu.setAttribute("aria-expanded", "false");
    bar.append(menu);
    return bar;
}

function hero(current: HomeHost, categories: Category[], notes: NoteSummary[]): HTMLElement {
    const section = el("header", "home-hero");
    section.append(text("p", "home-eyebrow", greeting()));
    section.append(text("h1", "home-title", "Where does this go?"));

    const journal = journalCategory(categories);
    const entry = todayEntry(journal, notes);
    const lede = journal
        ? "Pick a place to start. Each category states what belongs in it, so a model knows what to read and where to write over MCP."
        : "Pick a place to start. Categories keep notes apart so a model can read one topic, write into it and summarise it later.";
    section.append(text("p", "home-lede", lede));

    const actions = el("div", "home-actions");
    actions.append(button("btn primary", "New note", () => void current.newNote()));
    const journalButton = button(
        "btn",
        entry ? "Open today's log" : "Start today's log",
        () => void current.openTodayJournal(),
    );
    journalButton.classList.add("home-journal-btn");
    actions.append(journalButton);
    section.append(actions);

    const hint = el("p", "home-hint");
    hint.textContent = entry
        ? `Today's log exists — last edit ${new Date(entry.modified).toLocaleTimeString(undefined, {
              hour: "2-digit",
              minute: "2-digit",
          })}.`
        : "Today's log starts a dated entry in the Daily log category. A model can turn a run of entries into a track record.";
    section.append(hint);

    return section;
}

function categoriesSection(
    current: HomeHost,
    categories: Category[],
    notes: NoteSummary[],
    journal: Category | undefined,
): HTMLElement {
    const section = el("section", "home-section");
    const head = el("div", "home-section-head");
    head.append(text("h2", "home-section-title", "Categories"));
    head.append(button("link-btn", "Manage", () => openCategoryDialog()));
    section.append(head);

    const grid = el("div", "category-grid");
    if (categories.length === 0) {
        const empty = el("p", "home-empty");
        empty.textContent =
            "No categories yet. A category is how you tell the app — and a model — what a note is for.";
        grid.append(empty);
    }
    for (const category of categories) {
        grid.append(categoryCard(current, category, notes, journal));
    }
    grid.append(newCategoryCard());
    section.append(grid);
    return section;
}

function categoryCard(
    current: HomeHost,
    category: Category,
    notes: NoteSummary[],
    journal: Category | undefined,
): HTMLElement {
    const card = el("article", "category-card");
    card.dataset.categoryId = category.id;

    const head = el("div", "category-card-head");
    head.append(text("h3", "category-card-name", category.name));
    if (category.kind === "journal") {
        head.append(text("span", "category-card-tag", "Daily"));
    }
    const count = notesIn(category.id, notes).length;
    head.append(text("span", "category-card-count", count === 1 ? "1 note" : `${count} notes`));
    card.append(head);

    const purpose = text(
        "p",
        "category-card-purpose",
        category.description || "No purpose set yet. Describe what belongs here so an agent can use it.",
    );
    if (!category.description) purpose.classList.add("is-placeholder");
    card.append(purpose);

    const actions = el("div", "category-card-actions");
    actions.append(button("btn small", "Open", () => void current.filterNotes(category.id)));
    if (category.kind === "journal" && category.id === journal?.id) {
        actions.append(
            button("btn small primary", "Today", () => void current.openTodayJournal()),
        );
    } else {
        actions.append(button("btn small primary", "New note", () => void current.newNote(category.id)));
    }
    actions.append(button("link-btn", "Edit", () => openCategoryDialog(category)));
    card.append(actions);

    return card;
}

function newCategoryCard(): HTMLElement {
    const card = el("button", "category-card is-new");
    card.type = "button";
    card.append(text("span", "category-card-new-title", "New category"));
    card.append(text("span", "category-card-new-hint", "Name a place and say what belongs in it."));
    card.addEventListener("click", () => openCategoryDialog());
    return card;
}

function planSection(current: HomeHost, notes: NoteSummary[]): HTMLElement {
    const section = el("section", "home-section");
    const head = el("div", "home-section-head");
    head.append(text("h2", "home-section-title", "Plan"));
    section.append(head);

    const tiles = el("div", "home-tiles");
    tiles.append(
        shortcutTile("Queue", "Scheduled and overdue posts, oldest first.", "Open queue", () =>
            current.setScreen("queue"),
        ),
    );
    tiles.append(
        shortcutTile("Calendar", "The month at a glance, grouped by day.", "Open calendar", () =>
            current.setScreen("calendar"),
        ),
    );
    tiles.append(
        shortcutTile(
            "All notes",
            notes.length === 1 ? "1 note, newest first." : `${notes.length} notes, newest first.`,
            "Browse",
            () => void current.filterNotes(null),
        ),
    );
    section.append(tiles);
    return section;
}

function shortcutTile(title: string, description: string, action: string, onClick: () => void): HTMLElement {
    const tile = el("button", "home-tile");
    tile.type = "button";
    tile.append(text("span", "home-tile-title", title));
    tile.append(text("span", "home-tile-text", description));
    tile.append(text("span", "home-tile-action", action));
    tile.addEventListener("click", onClick);
    return tile;
}

function recentSection(current: HomeHost, notes: NoteSummary[]): HTMLElement {
    const section = el("section", "home-section");
    const head = el("div", "home-section-head");
    head.append(text("h2", "home-section-title", "Recent"));
    section.append(head);

    const recent = [...notes].sort((a, b) => b.modified - a.modified).slice(0, 4);
    if (recent.length === 0) {
        const empty = el("p", "home-empty");
        empty.textContent = "Nothing here yet. Start with today's log or a new note.";
        section.append(empty);
        return section;
    }

    const list = el("ul", "home-recent");
    for (const note of recent) {
        const item = el("li");
        const row = el("button", "home-recent-row");
        row.type = "button";
        row.dataset.id = note.id;
        row.append(text("span", "home-recent-title", note.title || "Untitled note"));
        row.append(text("span", "home-recent-meta", note.preview));
        row.addEventListener("click", () => void current.openNote(note.id));
        item.append(row);
        list.append(item);
    }
    section.append(list);
    return section;
}

// ---------------------------------------------------------------------------
// Category dialog
// ---------------------------------------------------------------------------

export function openCategoryDialog(category?: Category): void {
    const modal = document.getElementById("category-modal") as HTMLDivElement | null;
    if (!modal) return;
    dialogReturnFocus = document.activeElement as HTMLElement | null;
    setEditing(category ?? null);
    renderCategoryManageList();
    modal.hidden = false;
    (document.getElementById("category-name") as HTMLInputElement | null)?.focus();
}

function closeCategoryDialog(): void {
    const modal = document.getElementById("category-modal") as HTMLDivElement | null;
    if (!modal || modal.hidden) return;
    modal.hidden = true;
    setEditing(null);
    dialogReturnFocus?.focus();
    dialogReturnFocus = null;
}

function setEditing(category: Category | null): void {
    editingID = category?.id ?? null;

    const name = document.getElementById("category-name") as HTMLInputElement | null;
    const description = document.getElementById("category-description") as HTMLTextAreaElement | null;
    const title = document.getElementById("category-form-title");
    const save = document.getElementById("category-save") as HTMLButtonElement | null;
    const reset = document.getElementById("category-reset") as HTMLButtonElement | null;

    if (name) name.value = category?.name ?? "";
    if (description) description.value = category?.description ?? "";
    if (title) title.textContent = category ? `Editing “${category.name}”` : "New category";
    if (save) save.textContent = category ? "Save changes" : "Create category";
    if (reset) reset.hidden = !category;
    hideCategoryError();
}

function renderCategoryManageList(): void {
    const list = document.getElementById("category-manage-list") as HTMLUListElement | null;
    if (!list) return;
    const { categories, notes } = requireHost().data();
    list.replaceChildren();

    if (categories.length === 0) {
        const empty = el("li", "category-manage-empty");
        empty.textContent = "No categories yet. Name one below.";
        list.append(empty);
        return;
    }

    for (const category of categories) {
        const row = el("li", "category-manage-row");
        const body = el("div", "category-manage-body");
        body.append(text("span", "category-manage-name", category.name));
        const count = notesIn(category.id, notes).length;
        body.append(
            text(
                "span",
                "category-manage-purpose",
                category.description || `${count === 1 ? "1 note" : `${count} notes`} · no purpose yet`,
            ),
        );

        const actions = el("div", "category-manage-actions");
        actions.append(
            button("link-btn", "Edit", () => {
                setEditing(category);
                (document.getElementById("category-name") as HTMLInputElement | null)?.focus();
            }),
        );
        actions.append(
            button("link-btn danger-text", "Delete", () => void deleteCategory(category)),
        );

        row.append(body, actions);
        list.append(row);
    }
}

async function deleteCategory(category: Category): Promise<void> {
    const current = requireHost();
    const count = notesIn(category.id, current.data().notes).length;
    const ok = await current.confirm(
        "Delete category",
        `Delete “${category.name}”? ${count === 1 ? "Its 1 note is" : `Its ${count} notes are`} kept and move${count === 1 ? "s" : ""} to Inbox.`,
        "Delete",
    );
    if (!ok) return;

    try {
        await NotesService.DeleteCategory(category.id);
        if (editingID === category.id) setEditing(null);
        await current.refresh();
        renderCategoryManageList();
        await renderHomeScreen();
        current.showToast("Category deleted; its notes are in Inbox");
    } catch (err) {
        showCategoryError(errorMessage(err));
    }
}

async function submitCategory(event: Event): Promise<void> {
    event.preventDefault();
    const current = requireHost();
    const nameInput = document.getElementById("category-name") as HTMLInputElement | null;
    const descriptionInput = document.getElementById("category-description") as HTMLTextAreaElement | null;
    const name = nameInput?.value.trim() ?? "";
    const description = descriptionInput?.value.trim() ?? "";

    if (!name) {
        showCategoryError("Give the category a name.");
        nameInput?.focus();
        return;
    }

    try {
        if (editingID) {
            await NotesService.UpdateCategory(editingID, name, description);
        } else {
            await NotesService.CreateCategory(name, description);
        }
        const message = editingID ? `Updated “${name}”` : `Created “${name}”`;
        setEditing(null);
        await current.refresh();
        renderCategoryManageList();
        await renderHomeScreen();
        current.showToast(message);
    } catch (err) {
        showCategoryError(errorMessage(err));
    }
}

function showCategoryError(message: string): void {
    const node = document.getElementById("category-error");
    if (!node) return;
    node.textContent = message;
    node.hidden = false;
}

function hideCategoryError(): void {
    const node = document.getElementById("category-error");
    if (!node) return;
    node.textContent = "";
    node.hidden = true;
}

function bindCategoryDialog(): void {
    const modal = document.getElementById("category-modal") as HTMLDivElement | null;
    const form = document.getElementById("category-form") as HTMLFormElement | null;
    if (!modal || !form) return;

    form.addEventListener("submit", (event) => void submitCategory(event));
    document.getElementById("category-cancel")?.addEventListener("click", closeCategoryDialog);
    document.getElementById("category-reset")?.addEventListener("click", () => {
        setEditing(null);
        (document.getElementById("category-name") as HTMLInputElement | null)?.focus();
    });
    modal.addEventListener("click", (event) => {
        if (event.target === modal) closeCategoryDialog();
    });
    document.addEventListener("keydown", (event) => {
        if (event.key === "Escape" && !modal.hidden) {
            event.preventDefault();
            closeCategoryDialog();
        }
    });
}
