import { Browser } from "@wailsio/runtime";
import * as PublishService from "../bindings/quire/publishservice.js";
import * as PlannerService from "../bindings/quire/plannerservice.js";
import type {
    Draft,
    PrepareOptions,
    ProviderStatus,
    PublishRequest,
    PublishState,
    Rendition,
} from "../bindings/quire/models.js";

// X and LinkedIn are local-only: the app shapes the text and hands it to the
// clipboard or the platform composer. dev.to and Hashnode publish directly with
// an API key that never leaves the Go side.

type SocialPlatform = "x" | "linkedin";
type BlogPlatform = "devto" | "hashnode";
type Platform = SocialPlatform | BlogPlatform;
type Mode = "thread" | "single";

interface Source {
    id: string;
    title: string;
    body: string;
    slotId?: string;
}

interface BlogForm {
    title: string;
    body: string;
    tags: string;
    canonical: string;
}

const modal = document.getElementById("publish-modal") as HTMLDivElement;
const sub = document.getElementById("publish-sub") as HTMLParagraphElement;
const tabs = document.getElementById("publish-tabs") as HTMLDivElement;

const socialPanel = document.getElementById("publish-social") as HTMLDivElement;
const linkInput = document.getElementById("publish-link") as HTMLInputElement;
const chooser = document.getElementById("publish-chooser") as HTMLDivElement;
const modeGroup = document.getElementById("publish-mode") as HTMLDivElement;
const numberWrap = document.getElementById("publish-number-wrap") as HTMLLabelElement;
const numberInput = document.getElementById("publish-number") as HTMLInputElement;
const fieldLabel = document.getElementById("publish-field-label") as HTMLSpanElement;
const counter = document.getElementById("publish-counter") as HTMLSpanElement;
const textArea = document.getElementById("publish-text") as HTMLTextAreaElement;
const partsContainer = document.getElementById("publish-parts") as HTMLDivElement;
const hint = document.getElementById("publish-hint") as HTMLParagraphElement;
const hook = document.getElementById("publish-hook") as HTMLParagraphElement;

const blogPanel = document.getElementById("publish-blog") as HTMLDivElement;
const connectPanel = document.getElementById("publish-connect") as HTMLDivElement;
const connectTitle = document.getElementById("publish-connect-title") as HTMLParagraphElement;
const connectHelp = document.getElementById("publish-connect-help") as HTMLParagraphElement;
const secretInput = document.getElementById("publish-secret") as HTMLInputElement;
const connectButton = document.getElementById("publish-connect-btn") as HTMLButtonElement;
const connectNote = document.getElementById("publish-connect-note") as HTMLParagraphElement;
const connectedPanel = document.getElementById("publish-connected") as HTMLDivElement;
const accountLabel = document.getElementById("publish-account") as HTMLSpanElement;
const storageNote = document.getElementById("publish-storage-note") as HTMLParagraphElement;
const disconnectButton = document.getElementById("publish-disconnect") as HTMLButtonElement;
const publicationWrap = document.getElementById("publish-publication-wrap") as HTMLLabelElement;
const publicationSelect = document.getElementById("publish-publication") as HTMLSelectElement;
const statusPanel = document.getElementById("publish-status") as HTMLDivElement;
const statusBadge = document.getElementById("publish-status-badge") as HTMLSpanElement;
const statusTime = document.getElementById("publish-status-time") as HTMLSpanElement;
const statusView = document.getElementById("publish-status-view") as HTMLButtonElement;
const statusUnlink = document.getElementById("publish-status-unlink") as HTMLButtonElement;
const blogTitle = document.getElementById("publish-blog-title") as HTMLInputElement;
const blogBody = document.getElementById("publish-blog-body") as HTMLTextAreaElement;
const blogTags = document.getElementById("publish-blog-tags") as HTMLInputElement;
const blogCanonical = document.getElementById("publish-blog-canonical") as HTMLInputElement;

const warnings = document.getElementById("publish-warnings") as HTMLUListElement;
const notice = document.getElementById("publish-notice") as HTMLParagraphElement;
const errorBox = document.getElementById("publish-error") as HTMLParagraphElement;
const cancelButton = document.getElementById("publish-cancel") as HTMLButtonElement;
const copyButton = document.getElementById("publish-copy") as HTMLButtonElement;
const openButton = document.getElementById("publish-open") as HTMLButtonElement;
const saveDraftButton = document.getElementById("publish-save-draft") as HTMLButtonElement;
const submitButton = document.getElementById("publish-submit") as HTMLButtonElement;
const saveSlotButton = document.getElementById("publish-save-slot") as HTMLButtonElement;
const regenerateButton = document.getElementById("publish-regenerate") as HTMLButtonElement;

const LINK_KEY = "notes.publishLink";
const CANONICAL_KEY = "notes.publishCanonical";
const URL_PATTERN = /https?:\/\/[^\s]+/g;

let platform: Platform = "x";
let mode: Mode = "thread";
let numberThreads = false;
let source: Source = { id: "", title: "", body: "" };
let draft: Draft | null = null;
let publishState: PublishState | null = null;
let connections: Partial<Record<BlogPlatform, ProviderStatus>> = {};
let connectionsLoaded = false;
let blogForms: Partial<Record<BlogPlatform, BlogForm>> = {};
let returnFocus: HTMLElement | null = null;
let prepareSeq = 0;
let busy = false;
let linkTimer: ReturnType<typeof setTimeout> | undefined;

// A publish dialog opened from the reminder banner or a queue row carries the
// slot it represents, so edits can be saved back as that slot's rendition.
interface SlotContext {
    noteId: string;
    slotId: string;
}

let slotContext: SlotContext | null = null;
let lockedPlatform: Platform | null = null;
let renditions: Partial<Record<Platform, Rendition>> = {};
let renditionCanonical: Partial<Record<Platform, string>> = {};
let blogRenditionApplied = new Set<Platform>();
let savedSnapshot = "";

// localStorage can be unavailable in some webviews; never break the dialog.
const storage = {
    get(key: string): string {
        try {
            return localStorage.getItem(key) ?? "";
        } catch {
            return "";
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

function isBlogPlatform(value: string): value is BlogPlatform {
    return value === "devto" || value === "hashnode";
}

function isSocialPlatform(value: Platform): value is SocialPlatform {
    return value === "x" || value === "linkedin";
}

function isPlatform(value: string): value is Platform {
    return value === "x" || value === "linkedin" || value === "devto" || value === "hashnode";
}

function platformName(value: Platform): string {
    switch (value) {
        case "x":
            return "X";
        case "linkedin":
            return "LinkedIn";
        case "devto":
            return "dev.to";
        case "hashnode":
            return "Hashnode";
    }
}

// X counts URLs as a fixed 23 characters; LinkedIn counts every character.
function weightedLength(value: string): number {
    let count = 0;
    let last = 0;
    for (const match of value.matchAll(URL_PATTERN)) {
        count += runeLength(value.slice(last, match.index));
        count += 23;
        last = (match.index ?? 0) + match[0].length;
    }
    return count + runeLength(value.slice(last));
}

function runeLength(value: string): number {
    return Array.from(value).length;
}

function paintCounter(element: HTMLElement, value: string, limit: number, weighted: boolean) {
    const count = weighted ? weightedLength(value) : runeLength(value);
    element.textContent = `${count} / ${limit}`;
    element.classList.toggle("is-over", count > limit);
    element.classList.toggle("is-near", count <= limit && count > limit * 0.9);
    return count;
}

function relativeTime(ms: number): string {
    if (!ms) return "just now";
    const diff = Date.now() - ms;
    const minute = 60_000;
    const hour = minute * 60;
    const day = hour * 24;
    if (diff < minute) return "just now";
    if (diff < hour) return `${Math.round(diff / minute)}m ago`;
    if (diff < day) return `${Math.round(diff / hour)}h ago`;
    return `${Math.round(diff / day)}d ago`;
}

// ---------------------------------------------------------------------------
// Opening and closing
// ---------------------------------------------------------------------------

export function isPublishOpen(): boolean {
    return !modal.hidden;
}

export function openPublishModal(input: Source) {
    source = input;
    slotContext = input.slotId ? { noteId: input.id, slotId: input.slotId } : null;
    lockedPlatform = null;
    renditions = {};
    renditionCanonical = {};
    blogRenditionApplied = new Set();
    savedSnapshot = "";
    platform = "x";
    mode = "thread";
    numberThreads = false;
    draft = null;
    publishState = null;
    connections = {};
    connectionsLoaded = false;
    blogForms = {};
    returnFocus = document.activeElement as HTMLElement | null;

    linkInput.value = storage.get(LINK_KEY);
    numberInput.checked = false;
    secretInput.value = "";
    hideMessages();
    syncTabs();
    syncMode();
    syncSlotActions();
    modal.hidden = false;
    if (slotContext) {
        void loadSlotContext();
    } else {
        void refresh(true);
    }
}

function closePublish(discard = false) {
    if (!discard && slotContext && slotDirty()) {
        if (!window.confirm("Discard unsaved changes to this slot?")) return;
    }
    modal.hidden = true;
    slotContext = null;
    lockedPlatform = null;
    renditions = {};
    renditionCanonical = {};
    blogRenditionApplied = new Set();
    savedSnapshot = "";
    draft = null;
    publishState = null;
    clearTimeout(linkTimer);
    returnFocus?.focus();
    returnFocus = null;
}

// Loads the selected slot so the dialog opens on its platform and prefills the
// saved rendition, if there is one.
async function loadSlotContext() {
    const ctx = slotContext;
    if (!ctx) return;
    try {
        const plan = await PlannerService.Plan(ctx.noteId);
        const slot = plan?.slots?.find((candidate) => candidate.id === ctx.slotId) ?? null;
        if (slot && isPlatform(slot.platform)) {
            platform = slot.platform;
            lockedPlatform = slot.platform;
            if (slot.rendition) {
                renditions[platform] = slot.rendition;
                renditionCanonical[platform] = slot.rendition.canonical ?? "";
                linkInput.value = slot.rendition.canonical ?? "";
            }
        }
    } catch (err) {
        showError(err);
    }
    syncTabs();
    syncMode();
    await refresh(true);
    savedSnapshot = snapshot();
    syncSlotActions();
}

// ---------------------------------------------------------------------------
// Slot context (renditions)
// ---------------------------------------------------------------------------

function syncSlotActions() {
    const hasSlot = Boolean(slotContext);
    saveSlotButton.hidden = !hasSlot;
    saveSlotButton.disabled = !hasSlot || busy;
    const hasRendition = hasSlot && Boolean(renditions[platform]);
    regenerateButton.hidden = !hasRendition;
    regenerateButton.disabled = !hasRendition || busy;
}

// Applies a saved rendition over a freshly prepared draft. Social slots take
// Text/Parts; blog slots take Title/Body/Tags. Both take Canonical.
function applyRendition(value: Platform, next: Draft) {
    const rendition = renditions[value];
    if (!rendition) return;

    if (isSocialPlatform(value)) {
        if (rendition.parts && rendition.parts.length > 0) {
            next.parts = rendition.parts;
            next.overLimit = true;
            mode = rendition.parts.length > 1 ? "thread" : "single";
        }
        if (rendition.text) {
            next.text = rendition.text;
        } else if (rendition.parts && rendition.parts.length > 0) {
            next.text = rendition.parts.join("\n\n");
        }
        next.truncated = false;
        return;
    }

    if (isBlogPlatform(value) && !blogRenditionApplied.has(value)) {
        blogForms[value] = {
            title: rendition.title || next.title,
            body: rendition.body ?? next.body,
            tags: (rendition.tags ?? []).join(", "),
            canonical: rendition.canonical ?? "",
        };
        blogRenditionApplied.add(value);
    }
}

// Reads the form currently on screen, in the same lane mapping the Go side
// stores. SourceHash/EditedAt are filled in by SaveRendition.
function buildRendition(): Rendition {
    if (isSocialPlatform(platform)) {
        const threaded = isThreaded();
        const parts = threaded
            ? Array.from(
                  partsContainer.querySelectorAll<HTMLTextAreaElement>("textarea"),
              )
                  .map((area) => area.value.trim())
                  .filter(Boolean)
            : [];
        return {
            text: threaded ? "" : textArea.value,
            parts,
            canonical: linkInput.value.trim() || renditionCanonical[platform] || "",
            sourceHash: "",
            editedAt: 0,
        };
    }

    const form = currentBlogForm();
    return {
        title: form?.title ?? "",
        body: form?.body ?? "",
        tags: readTags(form?.tags ?? ""),
        canonical: form?.canonical?.trim() ?? "",
        sourceHash: "",
        editedAt: 0,
    };
}

function snapshot(): string {
    return JSON.stringify(buildRendition());
}

function slotDirty(): boolean {
    if (!slotContext) return false;
    return snapshot() !== savedSnapshot;
}

async function saveToSlot(notify = true): Promise<boolean> {
    const ctx = slotContext;
    if (!ctx) return false;
    try {
        const state = await PlannerService.SaveRendition(ctx.noteId, ctx.slotId, buildRendition());
        const slot = state?.slots?.find((candidate) => candidate.id === ctx.slotId) ?? null;
        if (slot?.rendition) {
            renditions[platform] = slot.rendition;
            renditionCanonical[platform] = slot.rendition.canonical ?? "";
        }
        savedSnapshot = snapshot();
        syncSlotActions();
        if (notify) showNotice("Saved to slot.");
        return true;
    } catch (err) {
        showError(err);
        return false;
    }
}

// Copy, Open and Publish save first, so the queue never shows a stale draft
// after the user has acted on it.
async function ensureSlotSaved(): Promise<boolean> {
    if (!slotContext || !slotDirty()) return true;
    return saveToSlot(false);
}

function renderCurrent() {
    if (isSocialPlatform(platform)) renderSocial();
    else renderBlog();
}

async function regenerate() {
    if (!slotContext) return;
    if (slotDirty() && !window.confirm("Replace the edited draft with a fresh one from the note?")) {
        return;
    }
    regenerateButton.disabled = true;
    try {
        const next = await PublishService.Prepare(
            source.title,
            source.body,
            platform,
            { mode: "auto", numberThreads: false, canonical: linkInput.value.trim() },
        );
        draft = next;
        delete renditions[platform];
        delete renditionCanonical[platform];
        blogRenditionApplied.delete(platform);
        if (isBlogPlatform(platform)) {
            delete blogForms[platform];
        }
        if (isSocialPlatform(platform)) {
            mode = "thread";
        }
        renderCurrent();
        if (await saveToSlot(false)) {
            showNotice("Regenerated from the note and saved to the slot.");
        }
    } catch (err) {
        showError(err);
    } finally {
        syncSlotActions();
    }
}

// ---------------------------------------------------------------------------
// Data loading
// ---------------------------------------------------------------------------

async function loadConnections(force = false) {
    if (connectionsLoaded && !force) return;
    const list = await PublishService.Connections();
    connections = {};
    for (const status of list ?? []) {
        if (isBlogPlatform(status.platform)) {
            connections[status.platform] = status;
        }
    }
    connectionsLoaded = true;
}

async function refresh(focus = false) {
    const seq = ++prepareSeq;
    hideMessages();

    try {
        if (isSocialPlatform(platform)) {
            const options: PrepareOptions = {
                mode,
                numberThreads,
                canonical: linkInput.value.trim(),
            };
            const next = await PublishService.Prepare(
                source.title,
                source.body,
                platform,
                options,
            );
            if (seq !== prepareSeq) return;
            draft = next;
            applyRendition(platform, draft);
            renderSocial();
            if (focus) focusSocialEditor();
            return;
        }

        const next = await PublishService.Prepare(
            source.title,
            source.body,
            platform,
            { mode: "auto", numberThreads: false, canonical: "" },
        );
        if (seq !== prepareSeq) return;
        draft = next;
        applyRendition(platform, draft);

        await loadConnections();
        if (seq !== prepareSeq) return;

        publishState = source.id
            ? await PublishService.PublishState(source.id)
            : null;
        if (seq !== prepareSeq) return;

        renderBlog();
        if (focus) blogBody.focus();
    } catch (err) {
        if (seq === prepareSeq) showError(err);
    }
}

function focusSocialEditor() {
    const target = isThreaded()
        ? partsContainer.querySelector<HTMLTextAreaElement>("textarea")
        : textArea;
    target?.focus();
}

// ---------------------------------------------------------------------------
// Shared rendering
// ---------------------------------------------------------------------------

function syncTabs() {
    for (const button of tabs.querySelectorAll<HTMLButtonElement>("button[data-platform]")) {
        const active = button.dataset.platform === platform;
        const locked = lockedPlatform !== null && button.dataset.platform !== lockedPlatform;
        button.classList.toggle("is-active", active);
        button.setAttribute("aria-selected", String(active));
        button.disabled = locked;
    }
}

function syncMode() {
    for (const button of modeGroup.querySelectorAll<HTMLButtonElement>("button[data-mode]")) {
        button.classList.toggle("is-active", button.dataset.mode === mode);
    }
    numberWrap.hidden = mode !== "thread";
}

function hideMessages() {
    errorBox.hidden = true;
    errorBox.textContent = "";
    notice.hidden = true;
    notice.textContent = "";
}

function showError(err: unknown) {
    const message = err instanceof Error ? err.message : String(err);
    errorBox.textContent = message;
    errorBox.hidden = false;
    notice.hidden = true;
}

function showNotice(message: string) {
    if (!message) return;
    notice.textContent = message;
    notice.hidden = false;
    errorBox.hidden = true;
}

function setWarnings(items: string[]) {
    warnings.replaceChildren();
    warnings.hidden = items.length === 0;
    for (const item of items) {
        const li = document.createElement("li");
        li.textContent = item;
        warnings.append(li);
    }
}

// ---------------------------------------------------------------------------
// X / LinkedIn
// ---------------------------------------------------------------------------

function renderSocial() {
    if (!draft || !isSocialPlatform(platform)) return;

    socialPanel.hidden = false;
    blogPanel.hidden = true;
    sub.textContent =
        "Turn this note into a post. Nothing leaves your computer until you paste it.";
    cancelButton.textContent = "Cancel";
    copyButton.hidden = false;
    openButton.hidden = false;
    saveDraftButton.hidden = true;
    submitButton.hidden = true;
    syncSlotActions();

    textArea.disabled = false;
    const over = draft.overLimit && platform === "x";
    const threaded = over && mode === "thread";
    chooser.hidden = !over;
    syncMode();
    syncTabs();

    counter.classList.remove("is-over", "is-near");
    openButton.textContent = platform === "x" ? "Open X" : "Open LinkedIn";
    copyButton.textContent = threaded ? "Copy thread" : "Copy";

    setHook();
    setWarnings(socialWarnings(over));

    if (threaded) {
        const partCount = draft.parts?.length ?? 0;
        fieldLabel.textContent = `Thread (${partCount} parts)`;
        counter.textContent = `${partCount} parts`;
        textArea.hidden = true;
        partsContainer.hidden = false;
        hint.hidden = false;
        renderParts();
        return;
    }

    textArea.hidden = false;
    partsContainer.hidden = true;
    hint.hidden = true;
    partsContainer.replaceChildren();
    textArea.value = draft.text;
    fieldLabel.textContent = platform === "x" ? "Post" : "LinkedIn post";
    paintCounter(counter, draft.text, draft.charLimit, platform === "x");
}

function socialWarnings(over: boolean): string[] {
    const items = [...(draft?.warnings ?? [])];
    if (platform === "x" && draft?.truncated && (!over || mode === "single")) {
        items.push("Trimmed to fit one post.");
    }
    return items;
}

function setHook() {
    if (platform === "linkedin" && draft && draft.hook && runeLength(draft.text) > 240) {
        hook.hidden = false;
        hook.textContent = `Before “see more”: ${draft.hook}`;
        return;
    }
    hook.hidden = true;
    hook.textContent = "";
}

function renderParts() {
    if (!draft) return;
    const parts = draft.parts ?? [];
    const charLimit = draft.charLimit;
    partsContainer.replaceChildren();

    parts.forEach((value, index) => {
        const part = document.createElement("div");
        part.className = "publish-part";

        const head = document.createElement("div");
        head.className = "publish-part-head";

        const title = document.createElement("span");
        title.className = "publish-part-title";
        title.textContent = `Part ${index + 1} of ${parts.length}`;

        const partCounter = document.createElement("span");
        partCounter.className = "publish-counter publish-part-counter";

        const copy = document.createElement("button");
        copy.type = "button";
        copy.className = "publish-part-copy";
        copy.textContent = "Copy";

        const body = document.createElement("textarea");
        body.value = value;
        body.spellcheck = true;
        body.setAttribute("aria-label", `Part ${index + 1} of ${parts.length}`);

        paintCounter(partCounter, value, charLimit, true);
        body.addEventListener("input", () => {
            paintCounter(partCounter, body.value, charLimit, true);
            copy.textContent = "Copy";
        });
        copy.addEventListener("click", async () => {
            if (await copyText(body.value)) {
                copy.textContent = "Copied";
                setTimeout(() => (copy.textContent = "Copy"), 1600);
            }
        });

        head.append(title, partCounter, copy);
        part.append(head, body);
        partsContainer.append(part);
    });
}

function isThreaded(): boolean {
    return platform === "x" && !!draft?.overLimit && mode === "thread";
}

function primaryText(): string {
    if (isThreaded()) {
        return Array.from(partsContainer.querySelectorAll<HTMLTextAreaElement>("textarea"))
            .map((area) => area.value.trim())
            .filter(Boolean)
            .join("\n\n");
    }
    return textArea.value;
}

function firstPart(): string {
    if (isThreaded()) {
        return partsContainer.querySelector<HTMLTextAreaElement>("textarea")?.value ?? "";
    }
    return textArea.value;
}

async function copyText(value: string): Promise<boolean> {
    try {
        await navigator.clipboard.writeText(value);
        return true;
    } catch {
        const temp = document.createElement("textarea");
        temp.value = value;
        document.body.append(temp);
        temp.select();
        const ok = document.execCommand("copy");
        temp.remove();
        return ok;
    }
}

async function runCopy() {
    if (!(await ensureSlotSaved())) return;
    const value = primaryText();
    if (!value.trim()) return;
    if (!(await copyText(value))) return;
    const label = copyButton.textContent;
    copyButton.textContent = "Copied";
    setTimeout(() => (copyButton.textContent = label), 1600);
}

async function openComposer() {
    if (!(await ensureSlotSaved())) return;
    const value = firstPart().trim();
    if (!value) return;
    const url =
        platform === "x"
            ? `https://twitter.com/intent/tweet?text=${encodeURIComponent(value)}`
            : `https://www.linkedin.com/feed/?shareActive=true&text=${encodeURIComponent(value)}`;
    void Browser.OpenURL(url);
}

// ---------------------------------------------------------------------------
// dev.to / Hashnode
// ---------------------------------------------------------------------------

function ensureBlogForm(value: Draft): BlogForm {
    if (!isBlogPlatform(platform)) {
        return { title: "", body: "", tags: "", canonical: "" };
    }
    const existing = blogForms[platform];
    if (existing) return existing;

    const form: BlogForm = {
        title: value.title,
        body: value.body,
        tags: (value.tags ?? []).join(", "),
        canonical: storage.get(CANONICAL_KEY),
    };
    blogForms[platform] = form;
    return form;
}

function currentBlogForm(): BlogForm | null {
    if (!isBlogPlatform(platform)) return null;
    return blogForms[platform] ?? null;
}

function renderBlog() {
    if (!draft || !isBlogPlatform(platform)) return;

    socialPanel.hidden = true;
    blogPanel.hidden = false;
    sub.textContent =
        "Publish this note directly. Your API key is stored in the OS keychain and never leaves the app.";
    cancelButton.textContent = "Close";
    copyButton.hidden = true;
    openButton.hidden = true;
    syncTabs();
    syncSlotActions();

    const connection = connections[platform];
    const connected = !!connection?.connected;

    connectPanel.hidden = connected;
    connectedPanel.hidden = !connected;
    submitButton.hidden = !connected;
    submitButton.disabled = !connected || busy;
    saveDraftButton.disabled = busy;

    if (!connected) {
        saveDraftButton.hidden = true;
        renderConnect(platform);
        setWarnings(draft.warnings ?? []);
        return;
    }

    accountLabel.textContent = `Connected as ${connection?.account || platformName(platform)}`;
    storageNote.hidden = !connection?.warning;
    storageNote.textContent = connection?.warning ?? "";

    const publications = connection?.publications ?? [];
    const showPublications = platform === "hashnode" && publications.length > 1;
    publicationWrap.hidden = !showPublications;
    if (showPublications) {
        const selected = connection?.publicationId ?? "";
        publicationSelect.replaceChildren(
            ...publications.map((publication) => {
                const option = document.createElement("option");
                option.value = publication.id;
                option.textContent = publication.name || publication.id;
                option.selected = publication.id === selected;
                return option;
            }),
        );
    }

    const form = ensureBlogForm(draft);
    blogTitle.value = form.title;
    blogBody.value = form.body;
    blogTags.value = form.tags;
    blogCanonical.value = form.canonical;

    renderBlogStatus();
    setWarnings(draft.warnings ?? []);
}

function renderConnect(value: BlogPlatform) {
    if (value === "devto") {
        connectTitle.textContent = "Connect dev.to";
        connectHelp.textContent =
            "Create an API key in dev.to → Settings → Extensions → DEV Community API Keys. Drafts are saved privately; publishing is a separate click.";
        connectNote.hidden = true;
        connectNote.textContent = "";
        secretInput.placeholder = "Paste your dev.to API key";
    } else {
        connectTitle.textContent = "Connect Hashnode";
        connectHelp.textContent =
            "Hashnode's API requires an active Pro plan on your publication. Create a Personal Access Token in Hashnode → Account settings → Developer.";
        connectNote.hidden = false;
        connectNote.textContent =
            "API access is Pro-only since May 2026. Free publications cannot publish through the API.";
        secretInput.placeholder = "Paste your Hashnode token";
    }
}

function renderBlogStatus() {
    if (!isBlogPlatform(platform)) return;

    const target = publishState?.targets?.[platform];
    if (!target) {
        statusPanel.hidden = true;
        saveDraftButton.hidden = false;
        saveDraftButton.textContent = "Save draft";
        submitButton.textContent = "Publish";
        return;
    }

    statusPanel.hidden = false;
    const changed = !!(
        draft?.contentHash &&
        target.contentHash &&
        draft.contentHash !== target.contentHash
    );
    const published = target.status === "published";

    statusBadge.textContent = changed
        ? "Changed since last publish"
        : published
            ? "Published"
            : "Draft";
    statusBadge.className = `publish-status-badge ${published ? "is-published" : "is-draft"}${changed ? " is-changed" : ""}`;
    statusTime.textContent = `Synced ${relativeTime(target.syncedAt)}`;
    statusView.hidden = !target.url;

    if (published) {
        // Neither API can unpublish a live post; keep the actions honest.
        saveDraftButton.hidden = true;
        submitButton.textContent = "Update post";
    } else {
        saveDraftButton.hidden = false;
        saveDraftButton.textContent = "Update draft";
        submitButton.textContent = "Publish now";
    }
}

function readTags(value: string): string[] {
    return value
        .split(",")
        .map((tag) => tag.trim())
        .filter(Boolean);
}

async function submitBlog(asDraft: boolean) {
    if (!draft || !isBlogPlatform(platform) || busy) return;
    const form = currentBlogForm();
    if (!form) return;
    if (!(await ensureSlotSaved())) return;

    const target = publishState?.targets?.[platform];

    busy = true;
    submitButton.disabled = true;
    saveDraftButton.disabled = true;
    submitButton.textContent = asDraft
        ? "Saving…"
        : target
            ? "Updating…"
            : "Publishing…";

    const request: PublishRequest = {
        noteId: source.id,
        title: form.title.trim() || "Untitled note",
        body: form.body,
        tags: readTags(form.tags),
        canonical: form.canonical.trim(),
        draft: asDraft,
    };

    try {
        const result = target
            ? await PublishService.UpdatePublished(platform, request)
            : await PublishService.Publish(platform, request);
        publishState = result.state;
        // Use the hash of what was actually sent, otherwise an edit made only
        // in this dialog would look like an unsynced change right away.
        const publishedHash = result.state?.targets?.[platform]?.contentHash;
        if (publishedHash) {
            draft = { ...draft, contentHash: publishedHash };
        }
        if (result.warning) {
            showError(result.warning);
        } else {
            showNotice(result.status === "published" ? "Published." : "Draft saved.");
        }
        window.dispatchEvent(new CustomEvent("planner:refresh-statuses"));
    } catch (err) {
        showError(err);
    } finally {
        busy = false;
        renderBlog();
    }
}

async function connect() {
    if (busy || !isBlogPlatform(platform)) return;
    const secret = secretInput.value.trim();
    if (!secret) {
        showError(`Paste your ${platformName(platform)} API key first.`);
        return;
    }

    busy = true;
    connectButton.disabled = true;
    connectButton.textContent = "Connecting…";

    try {
        const status = await PublishService.Connect(platform, secret);
        connections[platform] = status;
        connectionsLoaded = true;
        secretInput.value = "";
        showNotice(`Connected to ${platformName(platform)}.`);
    } catch (err) {
        showError(err);
    } finally {
        busy = false;
        connectButton.disabled = false;
        connectButton.textContent = "Connect";
        if (isBlogPlatform(platform)) renderBlog();
    }
}

async function disconnect() {
    if (busy || !isBlogPlatform(platform)) return;
    const value = platform;
    busy = true;
    try {
        await PublishService.Disconnect(value);
        delete connections[value];
        connectionsLoaded = true;
        showNotice(`Disconnected ${platformName(value)}.`);
    } catch (err) {
        showError(err);
    } finally {
        busy = false;
        if (isBlogPlatform(platform)) renderBlog();
    }
}

async function changePublication(publicationID: string) {
    if (!isBlogPlatform(platform) || busy) return;
    try {
        const status = await PublishService.SetPublication(platform, publicationID);
        connections[platform] = status;
        renderBlog();
    } catch (err) {
        showError(err);
    }
}

async function unlink() {
    if (!isBlogPlatform(platform) || busy) return;
    busy = true;
    try {
        publishState = await PublishService.Unlink(platform, source.id);
        showNotice(`Unlinked ${platformName(platform)}. The remote post was not changed.`);
        window.dispatchEvent(new CustomEvent("planner:refresh-statuses"));
    } catch (err) {
        showError(err);
    } finally {
        busy = false;
        if (isBlogPlatform(platform)) renderBlog();
    }
}

function openRemotePost() {
    if (!isBlogPlatform(platform)) return;
    const url = publishState?.targets?.[platform]?.url;
    if (url) void Browser.OpenURL(url);
}

// ---------------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------------

tabs.addEventListener("click", (event) => {
    const button = (event.target as HTMLElement).closest<HTMLButtonElement>("button[data-platform]");
    if (!button) return;
    if (lockedPlatform !== null && button.dataset.platform !== lockedPlatform) return;
    platform = button.dataset.platform as Platform;
    syncTabs();
    void refresh();
});

modeGroup.addEventListener("click", (event) => {
    const button = (event.target as HTMLElement).closest<HTMLButtonElement>("button[data-mode]");
    if (!button) return;
    mode = button.dataset.mode as Mode;
    syncMode();
    void refresh();
});

numberInput.addEventListener("change", () => {
    numberThreads = numberInput.checked;
    void refresh();
});

linkInput.addEventListener("input", () => {
    const value = linkInput.value.trim();
    storage.set(LINK_KEY, value);
    clearTimeout(linkTimer);
    linkTimer = setTimeout(() => void refresh(), 350);
});

textArea.addEventListener("input", () => {
    if (!draft || !isSocialPlatform(platform)) return;
    paintCounter(counter, textArea.value, draft.charLimit, platform === "x");
});

blogTitle.addEventListener("input", () => {
    const form = currentBlogForm();
    if (form) form.title = blogTitle.value;
});

blogBody.addEventListener("input", () => {
    const form = currentBlogForm();
    if (form) form.body = blogBody.value;
});

blogTags.addEventListener("input", () => {
    const form = currentBlogForm();
    if (form) form.tags = blogTags.value;
});

blogCanonical.addEventListener("input", () => {
    const form = currentBlogForm();
    if (!form) return;
    form.canonical = blogCanonical.value;
    storage.set(CANONICAL_KEY, blogCanonical.value.trim());
});

connectButton.addEventListener("click", () => void connect());
secretInput.addEventListener("keydown", (event) => {
    if (event.key === "Enter") {
        event.preventDefault();
        void connect();
    }
});
disconnectButton.addEventListener("click", () => void disconnect());
publicationSelect.addEventListener("change", () => void changePublication(publicationSelect.value));
saveDraftButton.addEventListener("click", () => void submitBlog(true));
submitButton.addEventListener("click", () => void submitBlog(false));
statusView.addEventListener("click", openRemotePost);
statusUnlink.addEventListener("click", () => void unlink());

cancelButton.addEventListener("click", () => closePublish(true));
copyButton.addEventListener("click", () => void runCopy());
openButton.addEventListener("click", () => void openComposer());
saveSlotButton.addEventListener("click", () => void saveToSlot(true));
regenerateButton.addEventListener("click", () => void regenerate());
modal.addEventListener("click", (event) => {
    if (event.target === modal) closePublish();
});

document.addEventListener("keydown", (event) => {
    if (modal.hidden) return;
    if (event.key === "Escape") {
        event.preventDefault();
        closePublish();
    }
});
