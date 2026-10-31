import "katex/dist/katex.min.css";
import "./style.css";
import { Browser, Dialogs, Events } from "@wailsio/runtime";
import * as NotesService from "../bindings/post-planner/notesservice.js";
import * as PlannerService from "../bindings/post-planner/plannerservice.js";
import type { Note, NoteSummary } from "../bindings/post-planner/models.js";
import { isPublishOpen, openPublishModal } from "./publish.js";
import {
    initPlanner,
    openSchedulerForNote,
    renderPlannerScreen,
    type PlannerHost,
} from "./planner.js";

// ---------------------------------------------------------------------------
// DOM
// ---------------------------------------------------------------------------

const app = document.getElementById("app") as HTMLDivElement;
const noteList = document.getElementById("note-list") as HTMLUListElement;
const listEmpty = document.getElementById("list-empty") as HTMLParagraphElement;
const notesCount = document.getElementById("notes-count") as HTMLSpanElement;
const searchInput = document.getElementById("search") as HTMLInputElement;
const searchClear = document.getElementById("search-clear") as HTMLButtonElement;
const newButton = document.getElementById("new-note") as HTMLButtonElement;
const importButton = document.getElementById("import-note") as HTMLButtonElement;
const emptyNewButton = document.getElementById("empty-new") as HTMLButtonElement;
const openFolderButton = document.getElementById("open-folder") as HTMLButtonElement;
const titleInput = document.getElementById("note-title") as HTMLInputElement;
const editor = document.getElementById("editor") as HTMLTextAreaElement;
const preview = document.getElementById("preview") as HTMLElement;
const previewPane = document.querySelector(".preview-pane") as HTMLElement;
const saveStatus = document.getElementById("save-status") as HTMLSpanElement;
const deleteButton = document.getElementById("delete-note") as HTMLButtonElement;
const exportButton = document.getElementById("export-note") as HTMLButtonElement;
const publishButton = document.getElementById("publish-note") as HTMLButtonElement;
const scheduleButton = document.getElementById("schedule-note") as HTMLButtonElement;
const screenToggle = document.getElementById("screen-toggle") as HTMLDivElement;
const sidebar = document.getElementById("sidebar") as HTMLElement;
const sidebarBackdrop = document.getElementById("sidebar-backdrop") as HTMLDivElement;
const workspace = document.getElementById("workspace") as HTMLElement;
const topbarMore = document.getElementById("topbar-more") as HTMLButtonElement;
const topbarMenu = document.getElementById("topbar-menu") as HTMLDivElement;
const menuExport = document.getElementById("menu-export") as HTMLButtonElement;
const menuDelete = document.getElementById("menu-delete") as HTMLButtonElement;
const plannerView = document.getElementById("planner-view") as HTMLElement;
const exportModal = document.getElementById("export-modal") as HTMLDivElement;
const exportList = document.getElementById("export-list") as HTMLUListElement;
const exportSelectAll = document.getElementById("export-select-all") as HTMLInputElement;
const exportCount = document.getElementById("export-count") as HTMLSpanElement;
const exportFormats = document.getElementById("export-formats") as HTMLFieldSetElement;
const exportCancel = document.getElementById("export-cancel") as HTMLButtonElement;
const exportConfirm = document.getElementById("export-confirm") as HTMLButtonElement;
const printRoot = document.getElementById("print-root") as HTMLDivElement;
const viewToggle = document.getElementById("view-toggle") as HTMLDivElement;
const toolbar = document.getElementById("md-toolbar") as HTMLDivElement;
const wordCount = document.getElementById("word-count") as HTMLSpanElement;
const charCount = document.getElementById("char-count") as HTMLSpanElement;
const emptyState = document.getElementById("empty-state") as HTMLDivElement;
const toast = document.getElementById("toast") as HTMLDivElement;
const confirmModal = document.getElementById("confirm-modal") as HTMLDivElement;
const confirmTitle = document.getElementById("confirm-title") as HTMLHeadingElement;
const confirmMessage = document.getElementById("confirm-message") as HTMLParagraphElement;
const confirmCancel = document.getElementById("confirm-cancel") as HTMLButtonElement;
const confirmDelete = document.getElementById("confirm-delete") as HTMLButtonElement;
const diskBanner = document.getElementById("disk-banner") as HTMLDivElement;
const diskReload = document.getElementById("disk-reload") as HTMLButtonElement;
const diskKeep = document.getElementById("disk-keep") as HTMLButtonElement;

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------

let notes: NoteSummary[] = [];
let activeId: string | null = null;
let dirty = false;
let saveTimer: ReturnType<typeof setTimeout> | undefined;
let previewTimer: ReturnType<typeof setTimeout> | undefined;
let saveSeq = 0;
let previewSeq = 0;
let selectSeq = 0;
let toastTimer: ReturnType<typeof setTimeout> | undefined;
let confirmResolve: ((confirmed: boolean) => void) | null = null;
let confirmReturnFocus: HTMLElement | null = null;

type ViewMode = "edit" | "split" | "preview";
type Screen = "notes" | "queue" | "calendar";

const MOBILE_QUERY = "(max-width: 760px)";
const SPLIT_QUERY = "(min-width: 761px)";

const MODE_KEY = "notes.viewMode";
const LAST_NOTE_KEY = "notes.lastId";
const COLLAPSED_KEY = "notes.collapsedStatuses";

const STATUS_ORDER = ["overdue", "scheduled", "ready", "drafting", "idea", "posted", "published"] as const;
const STATUS_LABEL: Record<string, string> = {
    overdue: "Overdue",
    scheduled: "Scheduled",
    ready: "Ready",
    drafting: "Drafting",
    idea: "Idea",
    posted: "Posted",
    published: "Published",
    other: "Other",
};

let screen: Screen = "notes";
let statuses: Record<string, string> | null = null;
let collapsedStatuses = new Set<string>();
let preferredMode: ViewMode = "split";
const mobileQuery = window.matchMedia(MOBILE_QUERY);
const splitQuery = window.matchMedia(SPLIT_QUERY);

// Webviews occasionally restrict localStorage; never let that break the app.
const storage = {
    get(key: string): string | null {
        try {
            return localStorage.getItem(key);
        } catch {
            return null;
        }
    },
    set(key: string, value: string) {
        try {
            localStorage.setItem(key, value);
        } catch {
            /* ignore */
        }
    },
};

function loadCollapsedStatuses(): string[] {
    try {
        const raw = storage.get(COLLAPSED_KEY);
        const parsed = raw ? JSON.parse(raw) : [];
        return Array.isArray(parsed) ? parsed.filter((value) => typeof value === "string") : [];
    } catch {
        return [];
    }
}

function saveCollapsedStatuses() {
    storage.set(COLLAPSED_KEY, JSON.stringify([...collapsedStatuses]));
}

// ---------------------------------------------------------------------------
// Small utilities
// ---------------------------------------------------------------------------

function debouncePreview() {
    clearTimeout(previewTimer);
    previewTimer = setTimeout(renderPreview, 120);
}

function showToast(message: string) {
    toast.textContent = message;
    toast.classList.add("is-visible");
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => toast.classList.remove("is-visible"), 3200);
}

// Themed replacement for the native dialog, which would follow the OS theme
// rather than the app's dark theme.
function confirmDestructive(title: string, message: string, action = "Delete"): Promise<boolean> {
    return new Promise((resolve) => {
        confirmResolve = resolve;
        confirmReturnFocus = document.activeElement as HTMLElement | null;
        confirmTitle.textContent = title;
        confirmMessage.textContent = message;
        confirmDelete.textContent = action;
        confirmModal.hidden = false;
        confirmDelete.focus();
    });
}

function closeConfirm(confirmed: boolean) {
    if (!confirmResolve) return;
    const resolve = confirmResolve;
    confirmResolve = null;
    confirmModal.hidden = true;
    confirmReturnFocus?.focus();
    confirmReturnFocus = null;
    resolve(confirmed);
}

confirmCancel.addEventListener("click", () => closeConfirm(false));
confirmDelete.addEventListener("click", () => closeConfirm(true));
confirmModal.addEventListener("click", (event) => {
    if (event.target === confirmModal) closeConfirm(false);
});

function relativeTime(ms: number): string {
    if (!ms) return "";
    const diff = Date.now() - ms;
    const minute = 60_000;
    const hour = 60 * minute;
    const day = 24 * hour;

    if (diff < minute) return "just now";
    if (diff < hour) return `${Math.floor(diff / minute)}m ago`;
    if (diff < day) return `${Math.floor(diff / hour)}h ago`;
    if (diff < 7 * day) return `${Math.floor(diff / day)}d ago`;
    return new Date(ms).toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

function composeMarkdown(): string {
    const title = titleInput.value.trim();
    const body = editor.value.replace(/\r\n/g, "\n").trimEnd();
    const heading = title ? `# ${title}` : "";
    if (!heading) return body;
    return body ? `${heading}\n\n${body}` : `${heading}\n`;
}

function updateStats() {
    const text = editor.value.trim();
    const words = text ? text.split(/\s+/).length : 0;
    wordCount.textContent = `${words} ${words === 1 ? "word" : "words"}`;
    charCount.textContent = `${text.length} ${text.length === 1 ? "character" : "characters"}`;
}

function setBusy(busy: boolean) {
    app.classList.toggle("is-busy", busy);
}

// ---------------------------------------------------------------------------
// Mobile drawer and topbar menu
// ---------------------------------------------------------------------------

let sidebarReturnFocus: HTMLElement | null = null;

function isMobileViewport(): boolean {
    return mobileQuery.matches;
}

function sidebarOpen(): boolean {
    return app.classList.contains("is-sidebar-open");
}

function syncSidebarAria(open: boolean) {
    for (const button of document.querySelectorAll<HTMLElement>("[data-sidebar-toggle]")) {
        if (button.id === "sidebar-close") continue;
        button.setAttribute("aria-expanded", String(open));
    }
}

function syncSidebarInert() {
    if (isMobileViewport() && !sidebarOpen()) {
        sidebar.setAttribute("inert", "");
    } else {
        sidebar.removeAttribute("inert");
    }
}

function openSidebar() {
    if (!isMobileViewport() || sidebarOpen()) return;
    sidebarReturnFocus = document.activeElement as HTMLElement | null;
    closeTopbarMenu();
    app.classList.add("is-sidebar-open");
    sidebarBackdrop.hidden = false;
    workspace.setAttribute("inert", "");
    syncSidebarAria(true);
    syncSidebarInert();
    document.getElementById("sidebar-close")?.focus();
}

function closeSidebar(restoreFocus = false) {
    if (!sidebarOpen()) return;
    app.classList.remove("is-sidebar-open");
    sidebarBackdrop.hidden = true;
    workspace.removeAttribute("inert");
    syncSidebarAria(false);
    syncSidebarInert();
    const target = sidebarReturnFocus;
    sidebarReturnFocus = null;
    if (restoreFocus) target?.focus();
}

function toggleSidebar() {
    if (sidebarOpen()) {
        closeSidebar(true);
    } else {
        openSidebar();
    }
}

function closeTopbarMenu(restoreFocus = false) {
    if (topbarMenu.hidden) return;
    topbarMenu.hidden = true;
    topbarMore.setAttribute("aria-expanded", "false");
    if (restoreFocus) topbarMore.focus();
}

function toggleTopbarMenu() {
    const open = topbarMenu.hidden;
    if (open) closeSidebar();
    topbarMenu.hidden = !open;
    topbarMore.setAttribute("aria-expanded", String(open));
    if (open) topbarMenu.querySelector<HTMLButtonElement>("button:not(:disabled)")?.focus();
}

function syncLayout() {
    if (!isMobileViewport()) {
        closeSidebar(false);
        sidebarBackdrop.hidden = true;
    }
    syncSidebarInert();
    closeTopbarMenu(false);
}

// ---------------------------------------------------------------------------
// Note list
// ---------------------------------------------------------------------------

function filteredNotes(): NoteSummary[] {
    const query = searchInput.value.trim().toLowerCase();
    if (!query) return notes;
    return notes.filter(
        (note) =>
            note.title.toLowerCase().includes(query) ||
            note.preview.toLowerCase().includes(query),
    );
}

function createNoteItem(note: NoteSummary): HTMLLIElement {
    const item = document.createElement("li");
    const button = document.createElement("button");
    button.type = "button";
    button.className = "note-item";
    button.dataset.id = note.id;
    if (note.id === activeId) button.classList.add("is-active");

    const title = document.createElement("span");
    title.className = "note-item-title";
    title.textContent = note.title || "Untitled note";

    const meta = document.createElement("span");
    meta.className = "note-item-meta";
    const time = document.createElement("span");
    time.textContent = relativeTime(note.modified);
    const snippet = document.createElement("span");
    snippet.className = "note-item-preview";
    snippet.textContent = note.preview;
    meta.append(time, snippet);

    button.append(title, meta);
    button.addEventListener("click", () => void selectNote(note.id));
    item.append(button);
    return item;
}

function renderList() {
    const visible = filteredNotes();
    noteList.replaceChildren();

    if (statuses) {
        const groups = new Map<string, NoteSummary[]>();
        for (const note of visible) {
            const raw = statuses[note.id] ?? "other";
            const status = (STATUS_ORDER as readonly string[]).includes(raw) ? raw : "other";
            const bucket = groups.get(status) ?? [];
            bucket.push(note);
            groups.set(status, bucket);
        }
        const order = [
            ...STATUS_ORDER.filter((status) => groups.has(status)),
            ...(groups.has("other") ? ["other"] : []),
        ];
        for (const status of order) {
            const notesInGroup = groups.get(status) ?? [];
            const group = document.createElement("li");
            group.className = "note-group";
            group.dataset.status = status;

            const header = document.createElement("button");
            header.type = "button";
            header.className = "note-group-head";
            const collapsed = collapsedStatuses.has(status);
            header.setAttribute("aria-expanded", String(!collapsed));
            const caret = document.createElement("span");
            caret.className = "note-group-caret";
            caret.textContent = "▾";
            const label = document.createElement("span");
            label.textContent = STATUS_LABEL[status] ?? status;
            const count = document.createElement("span");
            count.className = "note-group-count";
            count.textContent = String(notesInGroup.length);
            header.append(caret, label, count);
            header.addEventListener("click", () => {
                if (collapsedStatuses.has(status)) {
                    collapsedStatuses.delete(status);
                } else {
                    collapsedStatuses.add(status);
                }
                saveCollapsedStatuses();
                renderList();
            });

            const list = document.createElement("ul");
            list.className = "note-group-list";
            for (const note of notesInGroup) list.append(createNoteItem(note));

            if (collapsed) group.classList.add("is-collapsed");
            group.append(header, list);
            noteList.append(group);
        }
    } else {
        for (const note of visible) noteList.append(createNoteItem(note));
    }

    listEmpty.hidden = visible.length !== 0 || notes.length === 0;
    searchClear.hidden = searchInput.value.length === 0;
    notesCount.textContent = `${notes.length} ${notes.length === 1 ? "note" : "notes"}`;
}

function updateActiveListItem() {
    if (!activeId) return;
    const button = noteList.querySelector<HTMLButtonElement>(`[data-id="${activeId}"]`);
    if (!button) return;
    const title = button.querySelector(".note-item-title");
    const snippet = button.querySelector(".note-item-preview");
    if (title) title.textContent = titleInput.value.trim() || "Untitled note";
    if (snippet) snippet.textContent = editor.value.trim().slice(0, 120) || "No additional text";
}

async function loadNotes() {
    try {
        notes = (await NotesService.List()) ?? [];
        await loadStatuses();
        renderList();
    } catch (err) {
        reportError("Could not load notes", err);
    }
}

async function loadStatuses() {
    try {
        const map = await PlannerService.Statuses();
        statuses = map
            ? (Object.fromEntries(
                  Object.entries(map).filter(([, value]) => typeof value === "string"),
              ) as Record<string, string>)
            : null;
    } catch {
        statuses = null;
        showToast("Could not load note statuses; showing an ungrouped list");
    }
}

async function refreshStatuses() {
    await loadStatuses();
    renderList();
}

// ---------------------------------------------------------------------------
// External changes
// ---------------------------------------------------------------------------

function hideDiskBanner() {
    diskBanner.hidden = true;
}

// Reload the open note without going through selectNote: the sidebar is already
// refreshed, and the editor should keep its scroll position.
async function reloadActiveFromDisk() {
    if (!activeId) return;
    const id = activeId;
    const token = ++selectSeq;
    try {
        const note: Note = await NotesService.Get(id);
        if (token !== selectSeq || activeId !== id) return;
        titleInput.value = note.title;
        editor.value = note.body;
        dirty = false;
        hideDiskBanner();
        setSaveStatus(note.modified ? `Saved ${relativeTime(note.modified)}` : "");
        updateStats();
        updateActiveListItem();
        await renderPreview();
        showToast("Reloaded changes from disk");
    } catch (err) {
        reportError("Could not reload note", err);
    }
}

interface ExternalChange {
    noteIds?: unknown;
    kinds?: unknown;
}

async function handleExternalChange(raw: unknown) {
    const change = (raw ?? {}) as ExternalChange;
    const ids = Array.isArray(change.noteIds)
        ? change.noteIds.filter((value): value is string => typeof value === "string")
        : [];
    const kinds = Array.isArray(change.kinds)
        ? change.kinds.filter((value): value is string => typeof value === "string")
        : [];
    if (ids.length === 0) return;

    await loadNotes();

    const id = activeId;
    if (!id) return;

    const bodyChanged = kinds.includes("body");
    if (!bodyChanged) {
        // Plan and publish sidecars only affect grouping and queue data, which
        // loadNotes just refreshed. Re-rendering the planner here would close
        // an open schedule popover for the app's own writes.
        return;
    }

    if (!notes.some((note) => note.id === id)) {
        activeId = null;
        dirty = false;
        hideDiskBanner();
        titleInput.value = "";
        editor.value = "";
        preview.replaceChildren();
        setEditorEnabled(false);
        updateStats();
        storage.set(LAST_NOTE_KEY, "");
        showToast("The open note was deleted on disk");
        return;
    }

    if (!ids.includes(id)) return;

    if (dirty) {
        diskBanner.hidden = false;
    } else {
        await reloadActiveFromDisk();
    }
}

// ---------------------------------------------------------------------------
// Selection / editing
// ---------------------------------------------------------------------------

function setEditorEnabled(enabled: boolean) {
    titleInput.disabled = !enabled;
    editor.disabled = !enabled;
    deleteButton.disabled = !enabled;
    menuDelete.disabled = !enabled;
    publishButton.disabled = !enabled;
    scheduleButton.disabled = !enabled;
    emptyState.hidden = enabled;
    app.classList.toggle("has-note", enabled);
}

async function selectNote(id: string, options: { force?: boolean } = {}) {
    if (!options.force && id === activeId) {
        if (isMobileViewport()) closeSidebar();
        return;
    }
    await flushSave();
    hideDiskBanner();

    const token = ++selectSeq;
    setBusy(true);
    try {
        const note: Note = await NotesService.Get(id);
        if (token !== selectSeq) return;

        activeId = note.id;
        titleInput.value = note.title;
        editor.value = note.body;
        dirty = false;
        setEditorEnabled(true);
        setSaveStatus(note.modified ? `Saved ${relativeTime(note.modified)}` : "");
        updateStats();
        updateActiveListItem();
        renderList();
        storage.set(LAST_NOTE_KEY, id);
        await renderPreview();
        if (isMobileViewport()) closeSidebar();
    } catch (err) {
        reportError("Could not open note", err);
    } finally {
        if (token === selectSeq) setBusy(false);
    }
}

function scheduleSave() {
    if (!activeId) return;
    dirty = true;
    setSaveStatus("Unsaved changes");
    updateStats();
    updateActiveListItem();
    debouncePreview();
    clearTimeout(saveTimer);
    saveTimer = setTimeout(() => void saveNow(), 700);
}

async function saveNow(): Promise<void> {
    if (!activeId || !dirty) return;
    const id = activeId;
    const title = titleInput.value;
    const body = editor.value;
    const seq = ++saveSeq;
    dirty = false;

    setSaveStatus("Saving…");
    try {
        const saved = await NotesService.Save(id, title, body);
        if (seq !== saveSeq) return;
        if (activeId === id) {
            if (!title.trim()) titleInput.value = saved.title;
            setSaveStatus("Saved just now");
            // Saving writes the editor's version, so a pending external change
            // has now been consciously overwritten.
            hideDiskBanner();
        }
        const summary = notes.find((note) => note.id === id);
        if (summary) {
            summary.title = saved.title;
            summary.modified = saved.modified;
            summary.preview = body.trim().slice(0, 120) || "No additional text";
            if (statuses) void refreshStatuses();
            else renderList();
        }
    } catch (err) {
        if (seq === saveSeq) {
            dirty = true;
            setSaveStatus("Save failed");
            reportError("Could not save note", err);
        }
    }
}

async function flushSave() {
    clearTimeout(saveTimer);
    if (dirty) await saveNow();
}

function setSaveStatus(message: string) {
    saveStatus.textContent = message;
}

function reportError(context: string, err: unknown) {
    console.error(context, err);
    const message = err instanceof Error ? err.message : String(err);
    showToast(`${context}: ${message}`);
}

// ---------------------------------------------------------------------------
// Create / delete
// ---------------------------------------------------------------------------

async function createNote() {
    await flushSave();
    try {
        const note = await NotesService.Create();
        await loadNotes();
        await selectNote(note.id, { force: true });
        renderPreview();
        titleInput.focus();
        titleInput.select();
    } catch (err) {
        reportError("Could not create note", err);
    }
}

async function importNotes() {
    await flushSave();
    try {
        const selected = await Dialogs.OpenFile({
            Title: "Import Markdown files",
            Message: "Choose one or more Markdown files to add to your notes.",
            ButtonText: "Import",
            AllowsMultipleSelection: true,
            Filters: [
                { DisplayName: "Markdown", Pattern: "*.md;*.markdown;*.mdown;*.mkd;*.txt" },
                { DisplayName: "All files", Pattern: "*" },
            ],
        });
        const paths = Array.isArray(selected) ? selected : selected ? [selected] : [];
        if (paths.length === 0) return;

        const imported = (await NotesService.Import(paths)) ?? [];
        if (imported.length === 0) return;

        await loadNotes();
        await selectNote(imported[0].id, { force: true });
        showToast(
            imported.length === 1
                ? `Imported “${imported[0].title}”`
                : `Imported ${imported.length} notes`,
        );
    } catch (err) {
        reportError("Could not import notes", err);
    }
}

// ---------------------------------------------------------------------------
// Export / print
// ---------------------------------------------------------------------------

function exportSafeName(title: string): string {
    const safe = title
        .replace(/[^\p{L}\p{N} ._-]+/gu, "-")
        .replace(/\s+/g, " ")
        .replace(/^[ .-]+|[ .-]+$/g, "")
        .slice(0, 80)
        .trim();
    return safe || "note";
}

function exportTimezoneStamp(): string {
    const now = new Date();
    const pad = (value: number) => String(value).padStart(2, "0");
    return `${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}`;
}

function currentExportFormat(): string {
    const checked = exportFormats.querySelector<HTMLInputElement>('input[name="export-format"]:checked');
    return checked?.value ?? "markdown";
}

function selectedExportIds(): string[] {
    return Array.from(
        exportList.querySelectorAll<HTMLInputElement>('input[type="checkbox"]:checked'),
    ).map((input) => input.value);
}

function updateExportCount() {
    const total = exportList.querySelectorAll<HTMLInputElement>('input[type="checkbox"]').length;
    const selected = selectedExportIds().length;

    exportSelectAll.checked = selected > 0 && selected === total;
    exportSelectAll.indeterminate = selected > 0 && selected < total;
    exportCount.textContent = `${selected} selected`;

    const printing = currentExportFormat() === "print";
    exportConfirm.disabled = selected === 0;
    exportConfirm.textContent =
        selected === 0 ? "Export" : printing ? `Print ${selected}` : `Export ${selected}`;
}

function setExportFormat(format: string) {
    for (const input of exportFormats.querySelectorAll<HTMLInputElement>('input[name="export-format"]')) {
        input.checked = input.value === format;
        input.closest(".format-option")?.classList.toggle("is-selected", input.checked);
    }
    updateExportCount();
}

function openExport() {
    if (notes.length === 0) {
        showToast("There are no notes to export");
        return;
    }
    if (!confirmModal.hidden) return;

    exportList.replaceChildren();
    for (const note of notes) {
        const item = document.createElement("li");
        const label = document.createElement("label");
        label.className = "export-row";

        const checkbox = document.createElement("input");
        checkbox.type = "checkbox";
        checkbox.value = note.id;
        checkbox.checked = activeId ? note.id === activeId : true;
        checkbox.addEventListener("change", updateExportCount);

        const text = document.createElement("span");
        text.className = "export-row-text";
        const title = document.createElement("span");
        title.className = "export-row-title";
        title.textContent = note.title || "Untitled note";
        const meta = document.createElement("span");
        meta.className = "export-row-meta";
        meta.textContent = `${relativeTime(note.modified)} · ${note.preview}`;
        text.append(title, meta);

        label.append(checkbox, text);
        item.append(label);
        exportList.append(item);
    }

    setExportFormat("markdown");
    exportModal.hidden = false;
    const firstChecked = exportList.querySelector<HTMLInputElement>('input[type="checkbox"]:checked');
    (firstChecked ?? exportList.querySelector<HTMLInputElement>('input[type="checkbox"]'))?.focus();
}

function closeExport() {
    exportModal.hidden = true;
}

async function runExport() {
    const ids = selectedExportIds();
    if (ids.length === 0) return;

    await flushSave();

    const format = currentExportFormat();
    if (format === "print") {
        closeExport();
        await printNotes(ids);
        return;
    }

    const multiple = ids.length > 1;
    const extension = multiple ? ".zip" : format === "html" ? ".html" : ".md";
    const firstTitle = notes.find((note) => note.id === ids[0])?.title ?? "note";
    const filename = multiple
        ? `notes-export-${exportTimezoneStamp()}.zip`
        : `${exportSafeName(firstTitle)}${extension}`;

    const filters = multiple
        ? [{ DisplayName: "Zip archive", Pattern: "*.zip" }]
        : format === "html"
            ? [{ DisplayName: "HTML document", Pattern: "*.html" }]
            : [{ DisplayName: "Markdown document", Pattern: "*.md" }];

    try {
        const destination = await Dialogs.SaveFile({
            Title: format === "html" ? "Export HTML" : "Export Markdown",
            Filename: filename,
            Filters: filters,
        });
        if (!destination) return;

        const target = destination.toLowerCase().endsWith(extension)
            ? destination
            : destination + extension;

        const count = await NotesService.Export(ids, format, target);
        closeExport();
        showToast(count === 1 ? "Exported 1 note" : `Exported ${count} notes`);
    } catch (err) {
        reportError("Could not export notes", err);
    }
}

async function printNotes(ids: string[]) {
    setBusy(true);
    try {
        printRoot.replaceChildren();
        for (const id of ids) {
            const note = await NotesService.Get(id);
            const title = (note.title || "Untitled note").replace(/\n/g, " ");
            const html = await NotesService.Render(`# ${title}\n\n${note.body}`);
            const article = document.createElement("article");
            article.className = "print-note markdown-body";
            article.innerHTML = html;
            await renderMermaidBlocks(article);
            printRoot.append(article);
        }

        // Print the prepared pages rather than the app chrome.
        window.addEventListener("afterprint", () => printRoot.replaceChildren(), { once: true });
        await new Promise((resolve) => setTimeout(resolve, 80));
        window.print();
    } catch (err) {
        reportError("Could not print notes", err);
    } finally {
        setBusy(false);
    }
}

async function deleteNote() {
    if (!activeId) return;
    const note = notes.find((n) => n.id === activeId);
    const label = note?.title || "this note";
    const answer = await confirmDestructive(
        "Delete note",
        `Delete “${label}”? This cannot be undone.`,
    );
    if (!answer) return;

    clearTimeout(saveTimer);
    dirty = false;
    const id = activeId;
    try {
        await NotesService.Delete(id);
        activeId = null;
        titleInput.value = "";
        editor.value = "";
        setEditorEnabled(false);
        setSaveStatus("");
        updateStats();
        await loadNotes();
        const next = notes[0];
        if (next) {
            await selectNote(next.id, { force: true });
        } else {
            preview.replaceChildren();
        }
        showToast("Note deleted");
    } catch (err) {
        reportError("Could not delete note", err);
    }
}

// ---------------------------------------------------------------------------
// Screens
// ---------------------------------------------------------------------------

function setScreen(next: Screen) {
    if (next !== "notes") void flushSave();
    closeSidebar(false);
    closeTopbarMenu(false);
    screen = next;
    app.dataset.screen = next;
    for (const button of screenToggle.querySelectorAll<HTMLButtonElement>("button")) {
        button.classList.toggle("is-active", button.dataset.screen === next);
    }
    plannerView.hidden = next === "notes";
    if (next !== "notes") void renderPlannerScreen(next);
}

const plannerHost: PlannerHost = {
    showToast,
    async openNote(id: string) {
        await flushSave();
        setScreen("notes");
        await selectNote(id, { force: true });
    },
    async openPublishForSlot(noteID: string, slotID: string) {
        await flushSave();
        setScreen("notes");
        await selectNote(noteID, { force: true });
        openPublishModal({
            id: noteID,
            title: titleInput.value,
            body: editor.value,
            slotId: slotID,
        });
    },
    flushSave,
    confirm: confirmDestructive,
    setScreen,
    refreshStatuses,
};

// ---------------------------------------------------------------------------
// Publish
// ---------------------------------------------------------------------------

async function publishNote() {
    if (!activeId) return;
    await flushSave();
    openPublishModal({ id: activeId, title: titleInput.value, body: editor.value });
}

function scheduleNote() {
    if (!activeId) return;
    void flushSave().then(() => openSchedulerForNote(activeId!, scheduleButton));
}

// ---------------------------------------------------------------------------
// Preview
// ---------------------------------------------------------------------------

async function renderPreview() {
    const markdown = composeMarkdown();
    const seq = ++previewSeq;
    try {
        const html = await NotesService.Render(markdown);
        if (seq !== previewSeq) return;
        preview.innerHTML = html;
        await decoratePreview(seq);
        if (app.dataset.mode === "split") {
            scrollSource = null;
            syncScroll(editor, previewPane);
        }
    } catch (err) {
        if (seq === previewSeq) reportError("Could not render Markdown", err);
    }
}

type MermaidApi = typeof import("mermaid").default;

let mermaidPromise: Promise<MermaidApi> | null = null;
let mermaidSeq = 0;

function loadMermaid(): Promise<MermaidApi> {
    if (!mermaidPromise) {
        mermaidPromise = import("mermaid").then(({ default: mermaid }) => {
            mermaid.initialize({
                startOnLoad: false,
                securityLevel: "strict",
                suppressErrorRendering: true,
                theme: "dark",
                darkMode: true,
                fontFamily: "inherit",
                themeVariables: {
                    background: "transparent",
                    primaryColor: "#1a222d",
                    primaryTextColor: "#dbe3ee",
                    primaryBorderColor: "#2c3642",
                    lineColor: "#8b97a8",
                    secondaryColor: "#202a36",
                    tertiaryColor: "#141a22",
                    fontSize: "14px",
                },
            });
            return mermaid;
        });
    }
    return mermaidPromise;
}

// Mermaid has to run in the webview. The Markdown renderer deliberately leaves
// ```mermaid fences as plain code blocks so this can replace them with an SVG.
async function renderMermaidBlocks(
    container: HTMLElement,
    isStale: () => boolean = () => false,
): Promise<void> {
    const codes = Array.from(
        container.querySelectorAll<HTMLElement>("pre > code.language-mermaid"),
    );
    if (codes.length === 0) return;

    let mermaid: MermaidApi;
    try {
        mermaid = await loadMermaid();
    } catch (err) {
        for (const code of codes) {
            const pre = code.parentElement;
            if (!pre) continue;
            const wrapper = document.createElement("div");
            wrapper.className = "mermaid-block is-error";
            wrapper.textContent = `Diagram error: ${err instanceof Error ? err.message : String(err)}`;
            pre.replaceWith(wrapper);
        }
        return;
    }

    for (const code of codes) {
        if (isStale()) return;
        const pre = code.parentElement;
        if (!pre) continue;
        const source = code.textContent ?? "";
        const wrapper = document.createElement("div");
        wrapper.className = "mermaid-block";
        pre.replaceWith(wrapper);
        try {
            const { svg } = await mermaid.render(`mermaid-${++mermaidSeq}`, source);
            if (isStale()) return;
            wrapper.innerHTML = svg;
        } catch (err) {
            if (isStale()) return;
            wrapper.classList.add("is-error");
            const message = err instanceof Error ? err.message : String(err);
            wrapper.textContent = `Diagram error: ${message}`;
        }
    }
}

async function decoratePreview(token = previewSeq) {
    // External links open in the user's browser instead of inside the webview.
    for (const anchor of preview.querySelectorAll<HTMLAnchorElement>('a[href^="http"]')) {
        anchor.target = "_blank";
        anchor.rel = "noreferrer noopener";
    }

    // Goldmark renders task-list checkboxes disabled; enable them so the preview
    // can toggle the matching `- [ ]` item in the source.
    for (const box of preview.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')) {
        box.disabled = false;
    }

    await renderMermaidBlocks(preview, () => token !== previewSeq);

    // Give every fenced code block a copy button.
    for (const pre of preview.querySelectorAll<HTMLPreElement>("pre")) {
        const wrapper = document.createElement("div");
        wrapper.className = "code-block";
        pre.replaceWith(wrapper);
        wrapper.append(pre);

        const button = document.createElement("button");
        button.type = "button";
        button.className = "code-copy";
        button.textContent = "Copy";
        button.addEventListener("click", () => void copyCode(pre, button));
        wrapper.append(button);
    }
}

async function copyCode(pre: HTMLPreElement, button: HTMLButtonElement) {
    const text = pre.innerText;
    try {
        await navigator.clipboard.writeText(text);
        button.textContent = "Copied";
    } catch {
        const temp = document.createElement("textarea");
        temp.value = text;
        document.body.append(temp);
        temp.select();
        document.execCommand("copy");
        temp.remove();
        button.textContent = "Copied";
    }
    setTimeout(() => (button.textContent = "Copy"), 1600);
}

// ---------------------------------------------------------------------------
// Markdown toolbar
// ---------------------------------------------------------------------------

function wrapSelection(before: string, after: string, placeholder: string) {
    const start = editor.selectionStart;
    const end = editor.selectionEnd;
    const selected = editor.value.slice(start, end) || placeholder;
    editor.setRangeText(before + selected + after, start, end, "end");
    if (start === end) {
        editor.setSelectionRange(start + before.length, start + before.length + placeholder.length);
    }
    editor.dispatchEvent(new Event("input", { bubbles: true }));
    editor.focus();
}

function prefixLines(prefix: string | ((index: number) => string)) {
    const start = editor.selectionStart;
    const end = editor.selectionEnd;
    const value = editor.value;
    const lineStart = value.lastIndexOf("\n", start - 1) + 1;
    const lineEndIndex = value.indexOf("\n", end);
    const lineEnd = lineEndIndex === -1 ? value.length : lineEndIndex;
    const block = value.slice(lineStart, lineEnd);
    const lines = block.split("\n");
    const updated = lines.map((line, index) => {
        const marker = typeof prefix === "function" ? prefix(index) : prefix;
        return marker + line.replace(/^(#{1,6} |> |- \[[ x]\] |- |\d+\. )/, "");
    });
    editor.setRangeText(updated.join("\n"), lineStart, lineEnd, "end");
    editor.dispatchEvent(new Event("input", { bubbles: true }));
    editor.focus();
}

function insertBlock(block: string) {
    const start = editor.selectionStart;
    const value = editor.value;
    const needsLeading = start > 0 && value[start - 1] !== "\n";
    const text = `${needsLeading ? "\n" : ""}${block}`;
    editor.setRangeText(text, start, editor.selectionEnd, "end");
    editor.dispatchEvent(new Event("input", { bubbles: true }));
    editor.focus();
}

function insertLink() {
    const start = editor.selectionStart;
    const end = editor.selectionEnd;
    const label = editor.value.slice(start, end) || "link text";
    const snippet = `[${label}](https://)`;
    editor.setRangeText(snippet, start, end, "end");
    const urlStart = start + label.length + 3;
    editor.setSelectionRange(urlStart, urlStart + "https://".length);
    editor.dispatchEvent(new Event("input", { bubbles: true }));
    editor.focus();
}

function runToolbarAction(action: string) {
    switch (action) {
        case "bold":
            wrapSelection("**", "**", "bold text");
            break;
        case "italic":
            wrapSelection("*", "*", "italic text");
            break;
        case "strike":
            wrapSelection("~~", "~~", "strikethrough");
            break;
        case "h1":
            prefixLines("# ");
            break;
        case "h2":
            prefixLines("## ");
            break;
        case "quote":
            prefixLines((index) => (index === 0 ? "> " : ""));
            break;
        case "ul":
            prefixLines("- ");
            break;
        case "task":
            prefixLines("- [ ] ");
            break;
        case "code":
            wrapSelection("`", "`", "code");
            break;
        case "codeblock":
            insertBlock("```\ncode\n```\n");
            break;
        case "link":
            insertLink();
            break;
        case "table":
            insertBlock("| Column | Column |\n| --- | --- |\n| Cell | Cell |\n");
            break;
        case "math":
            wrapSelection("$", "$", "E = mc^2");
            break;
        case "mathblock":
            insertBlock("$$\n\\frac{a}{b}\n$$\n");
            break;
        case "mermaid":
            insertBlock("```mermaid\nflowchart LR\n  A[Start] --> B[End]\n```\n");
            break;
    }
}

toolbar.addEventListener("click", (event) => {
    const button = (event.target as HTMLElement).closest<HTMLButtonElement>("button[data-action]");
    if (button) runToolbarAction(button.dataset.action!);
});

// ---------------------------------------------------------------------------
// View mode
// ---------------------------------------------------------------------------

function applyViewMode() {
    const effective: ViewMode =
        preferredMode === "split" && !splitQuery.matches ? "edit" : preferredMode;
    app.dataset.mode = effective;
    for (const button of viewToggle.querySelectorAll<HTMLButtonElement>("button")) {
        button.classList.toggle("is-active", button.dataset.mode === effective);
    }
    if (effective === "edit") return;
    void renderPreview().then(() => {
        if (app.dataset.mode === "split") {
            scrollSource = null;
            syncScroll(editor, previewPane);
        }
    });
}

function setMode(mode: ViewMode) {
    if (mode === "split" && !splitQuery.matches) return;
    preferredMode = mode;
    storage.set(MODE_KEY, mode);
    applyViewMode();
}

function cycleMode() {
    const modes: ViewMode[] = splitQuery.matches
        ? ["edit", "split", "preview"]
        : ["edit", "preview"];
    const current = (app.dataset.mode as ViewMode) || "edit";
    const index = modes.indexOf(current);
    setMode(modes[(index + 1) % modes.length]);
}

viewToggle.addEventListener("click", (event) => {
    const button = (event.target as HTMLElement).closest<HTMLButtonElement>("button[data-mode]");
    if (button) setMode(button.dataset.mode as ViewMode);
});

// ---------------------------------------------------------------------------
// Split scroll sync
// ---------------------------------------------------------------------------

let scrollSource: HTMLElement | null = null;
let scrollSourceTimer: ReturnType<typeof setTimeout> | undefined;

// Proportional sync: both panes map 0..1 of their scrollable range onto each
// other. It is cheap, survives re-renders, and stays correct when the two
// panes have different heights (code blocks, images, padding).
function syncScroll(from: HTMLElement, to: HTMLElement) {
    if (app.dataset.mode !== "split") return;
    // Ignore the scroll event caused by assigning scrollTop on the other pane.
    if (scrollSource && scrollSource !== from) return;
    const fromMax = from.scrollHeight - from.clientHeight;
    const toMax = to.scrollHeight - to.clientHeight;
    if (fromMax <= 0 || toMax <= 0) return;
    const ratio = Math.min(1, Math.max(0, from.scrollTop / fromMax));
    scrollSource = from;
    to.scrollTop = ratio * toMax;
    clearTimeout(scrollSourceTimer);
    scrollSourceTimer = setTimeout(() => {
        scrollSource = null;
    }, 100);
}

editor.addEventListener("scroll", () => syncScroll(editor, previewPane), { passive: true });
previewPane.addEventListener("scroll", () => syncScroll(previewPane, editor), { passive: true });

// Sidebar toggles are rendered both by the shell and by the planner, so use
// event delegation rather than binding each button.
document.addEventListener("click", (event) => {
    const toggle = (event.target as HTMLElement).closest<HTMLElement>("[data-sidebar-toggle]");
    if (toggle) {
        event.preventDefault();
        toggleSidebar();
        return;
    }
    if (!topbarMenu.hidden && !topbarMenu.contains(event.target as Node) && event.target !== topbarMore) {
        closeTopbarMenu(false);
    }
});

sidebarBackdrop.addEventListener("click", () => closeSidebar(true));

topbarMore.addEventListener("click", (event) => {
    event.stopPropagation();
    toggleTopbarMenu();
});

topbarMenu.addEventListener("click", (event) => event.stopPropagation());

menuExport.addEventListener("click", () => {
    closeTopbarMenu(false);
    openExport();
});

menuDelete.addEventListener("click", () => {
    closeTopbarMenu(false);
    void deleteNote();
});

function onMediaChange(query: MediaQueryList, handler: () => void) {
    if (typeof query.addEventListener === "function") {
        query.addEventListener("change", handler);
    } else {
        query.addListener(handler);
    }
}

onMediaChange(splitQuery, applyViewMode);
onMediaChange(mobileQuery, syncLayout);

window.addEventListener("resize", () => {
    // Media-query events cover the breakpoint flips; this keeps the drawer
    // state honest when a webview misses the event.
    if (!isMobileViewport()) syncLayout();
});

// ---------------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------------

titleInput.addEventListener("input", () => {
    scheduleSave();
});

editor.addEventListener("input", () => {
    scheduleSave();
});

// Tabbing inside the editor inserts spaces instead of leaving the field.
editor.addEventListener("keydown", (event) => {
    if (event.key !== "Tab") return;
    event.preventDefault();
    const start = editor.selectionStart;
    const end = editor.selectionEnd;
    editor.setRangeText("  ", start, end, "end");
    editor.dispatchEvent(new Event("input", { bubbles: true }));
});

searchInput.addEventListener("input", renderList);
searchClear.addEventListener("click", () => {
    searchInput.value = "";
    searchInput.focus();
    renderList();
});

newButton.addEventListener("click", () => void createNote());
importButton.addEventListener("click", () => void importNotes());
emptyNewButton.addEventListener("click", () => void createNote());
deleteButton.addEventListener("click", () => void deleteNote());
publishButton.addEventListener("click", () => void publishNote());
scheduleButton.addEventListener("click", scheduleNote);
screenToggle.addEventListener("click", (event) => {
    const button = (event.target as HTMLElement).closest<HTMLButtonElement>("button[data-screen]");
    if (button) setScreen(button.dataset.screen as Screen);
});
window.addEventListener("planner:refresh-statuses", () => void refreshStatuses());
Events.On("notes:external-change", (event) => {
    void handleExternalChange(event.data);
});
diskReload.addEventListener("click", () => void reloadActiveFromDisk());
diskKeep.addEventListener("click", () => hideDiskBanner());
exportButton.addEventListener("click", openExport);
exportCancel.addEventListener("click", closeExport);
exportConfirm.addEventListener("click", () => void runExport());
exportModal.addEventListener("click", (event) => {
    if (event.target === exportModal) closeExport();
});
exportSelectAll.addEventListener("change", () => {
    for (const input of exportList.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')) {
        input.checked = exportSelectAll.checked;
    }
    updateExportCount();
});
exportFormats.addEventListener("change", () => {
    for (const input of exportFormats.querySelectorAll<HTMLInputElement>('input[name="export-format"]')) {
        input.closest(".format-option")?.classList.toggle("is-selected", input.checked);
    }
    updateExportCount();
});

openFolderButton.addEventListener("click", async () => {
    try {
        const path = await NotesService.NotesPath();
        if (navigator.clipboard) {
            await navigator.clipboard.writeText(path).catch(() => undefined);
            showToast(`Notes folder copied to clipboard: ${path}`);
        } else {
            showToast(`Notes folder: ${path}`);
        }
    } catch (err) {
        reportError("Could not locate notes folder", err);
    }
});

preview.addEventListener("click", (event) => {
    const anchor = (event.target as HTMLElement).closest<HTMLAnchorElement>("a[href]");
    if (!anchor) return;
    const href = anchor.getAttribute("href") || "";
    if (href.startsWith("http://") || href.startsWith("https://")) {
        event.preventDefault();
        void Browser.OpenURL(href);
    }
});

// Let checkbox clicks in the preview toggle the source Markdown task item.
preview.addEventListener("change", (event) => {
    const input = event.target as HTMLInputElement;
    if (input.type !== "checkbox") return;
    const boxes = Array.from(preview.querySelectorAll<HTMLInputElement>('input[type="checkbox"]'));
    const index = boxes.indexOf(input);
    if (index < 0) return;

    let seen = -1;
    editor.value = editor.value.replace(/^(\s*[-*+] )\[([ xX])\]/gm, (match, prefix: string) => {
        seen += 1;
        if (seen !== index) return match;
        return `${prefix}[${input.checked ? "x" : " "}]`;
    });
    editor.dispatchEvent(new Event("input", { bubbles: true }));
});

document.addEventListener("keydown", (event) => {
    // The drawer and mobile menu sit above the editor, so Escape closes them
    // before any other surface gets a chance to react.
    if (event.key === "Escape") {
        if (sidebarOpen()) {
            event.preventDefault();
            closeSidebar(true);
            return;
        }
        if (!topbarMenu.hidden) {
            event.preventDefault();
            closeTopbarMenu(true);
            return;
        }
    }

    // The publish dialog owns the keyboard while it is open.
    if (isPublishOpen()) return;

    // Planner surfaces handle their own shortcuts and forms.
    if (screen !== "notes") return;

    // The export modal owns Escape while it is open.
    if (!exportModal.hidden) {
        if (event.key === "Escape") {
            event.preventDefault();
            closeExport();
        }
        return;
    }

    // While the confirmation modal is open it owns the keyboard.
    if (!confirmModal.hidden) {
        if (event.key === "Escape") {
            event.preventDefault();
            closeConfirm(false);
        } else if (event.key === "Enter" && document.activeElement !== confirmCancel) {
            event.preventDefault();
            closeConfirm(true);
        }
        return;
    }

    const mod = event.metaKey || event.ctrlKey;
    if (!mod) return;

    switch (event.key.toLowerCase()) {
        case "n":
            event.preventDefault();
            void createNote();
            break;
        case "s":
            event.preventDefault();
            void flushSave();
            break;
        case "e":
            event.preventDefault();
            if (event.shiftKey) {
                openExport();
            } else {
                cycleMode();
            }
            break;
        case "f":
            event.preventDefault();
            searchInput.focus();
            searchInput.select();
            break;
        case "p":
            if (event.shiftKey) {
                event.preventDefault();
                void publishNote();
            }
            break;
        case "b":
            if (document.activeElement === editor) {
                event.preventDefault();
                runToolbarAction("bold");
            }
            break;
        case "i":
            if (document.activeElement === editor) {
                event.preventDefault();
                runToolbarAction("italic");
            }
            break;
        case "k":
            if (document.activeElement === editor) {
                event.preventDefault();
                runToolbarAction("link");
            }
            break;
    }
});

// Best-effort save when the window loses focus (e.g. app is closing).
window.addEventListener("blur", () => void flushSave());

// Fallback for filesystems where the watcher cannot start: refresh the list
// when the user comes back to the app.
window.addEventListener("focus", () => void loadNotes());

// ---------------------------------------------------------------------------
// Boot
// ---------------------------------------------------------------------------

async function boot() {
    const storedMode = storage.get(MODE_KEY);
    preferredMode =
        storedMode === "edit" || storedMode === "split" || storedMode === "preview"
            ? storedMode
            : "split";
    applyViewMode();
    syncSidebarAria(false);
    syncSidebarInert();
    closeTopbarMenu(false);
    setEditorEnabled(false);
    updateStats();
    collapsedStatuses = new Set(loadCollapsedStatuses());
    initPlanner(plannerHost);
    setScreen("notes");

    await loadNotes();

    const lastId = storage.get(LAST_NOTE_KEY);
    const initial = notes.find((note) => note.id === lastId) ?? notes[0];
    if (initial) {
        await selectNote(initial.id, { force: true });
    } else {
        renderList();
    }
}

void boot();
