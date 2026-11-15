"use client";

import * as React from "react";

import Link from "next/link";

import { ArrowUpCircleIcon } from "lucide-react";

import { api } from "@/lib/api";

/**
 * Top bar "new version available" badge: checks once on full page load, lights up next to the version number
 * when an update is available, and links directly to the "Version & Updates" card on the system settings page.
 *
 * The backend caches GitHub check results for 30 minutes, so querying once per mount here is safe —
 * unauthenticated GitHub API requests are limited to 60/hour/IP, and without that cache, opening a few tabs
 * would exhaust the quota and prevent actual update checks.
 *
 * Query failures are always silent: the top bar is not the place for errors; users will see the reason when clicking "Check for Updates" in Settings.
 */
export function UpdateBadge() {
  const [latest, setLatest] = React.useState("");

  React.useEffect(() => {
    let alive = true;
    api
      .checkUpdate()
      .then((r) => {
        // has_update already includes the "comparable version" check, so dev builds won't show this badge.
        if (alive && r.has_update && r.latest) setLatest(r.latest.replace(/^v(?=\d)/, ""));
      })
      .catch(() => {
        // Silent: offline or GitHub rate-limiting shouldn't pop up errors in the top bar.
      });
    return () => {
      alive = false;
    };
  }, []);

  if (!latest) return null;

  return (
    <Link
      href="/system/settings"
      title={`New version ${latest} available, click to update`}
      className="inline-flex items-center gap-1.5 rounded-full bg-primary px-2.5 py-1 font-medium text-primary-foreground text-xs transition-opacity hover:opacity-90"
    >
      {/* Pulsing dot: the top bar has many elements and plain text is easy to miss; animation makes it immediately noticeable. */}
      <span className="relative flex size-1.5">
        <span className="absolute inline-flex size-full animate-ping rounded-full bg-primary-foreground opacity-75" />
        <span className="relative inline-flex size-1.5 rounded-full bg-primary-foreground" />
      </span>
      <ArrowUpCircleIcon className="size-3.5" />
      <span className="hidden sm:inline">New {latest}</span>
      <span className="sm:hidden">Update</span>
    </Link>
  );
}
