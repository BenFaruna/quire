export type Platform = "x" | "linkedin" | "devto" | "hashnode";

export interface PlatformInfo {
    id: Platform;
    label: string;
    abbr: string;
}

// Order matches the publish dialog's tabs.
export const PLATFORMS: PlatformInfo[] = [
    { id: "x", label: "X", abbr: "X" },
    { id: "linkedin", label: "LinkedIn", abbr: "in" },
    { id: "devto", label: "dev.to", abbr: "dev" },
    { id: "hashnode", label: "Hashnode", abbr: "hash" },
];

export function isPlatform(value: string): value is Platform {
    return value === "x" || value === "linkedin" || value === "devto" || value === "hashnode";
}

export function platformLabel(value: string): string {
    return PLATFORMS.find((platform) => platform.id === value)?.label ?? value;
}

export function platformAbbr(value: string): string {
    return PLATFORMS.find((platform) => platform.id === value)?.abbr ?? value.slice(0, 4);
}
