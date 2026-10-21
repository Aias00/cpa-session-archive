import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) { return twMerge(clsx(inputs)); }

// Decode transport-escaped source previews; render the result as React text.
export function displayText(value: unknown): string {
  let text = String(value ?? "");
  for (let pass = 0; pass < 4; pass++) {
    const element = document.createElement("textarea");
    element.innerHTML = text;
    const decoded = element.value;
    if (decoded === text) break;
    text = decoded;
  }
  return text;
}
