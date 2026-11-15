"use client";

import { useEffect, useRef, useState } from "react";

import { useRouter } from "next/navigation";

import { AlertTriangle, ShieldCheck } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogClose, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import { auth } from "@/lib/auth";

export default function LoginPage() {
  const router = useRouter();
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [checking, setChecking] = useState(true);
  const [agreed, setAgreed] = useState(false);
  const [termsOpen, setTermsOpen] = useState(false);
  const [readToEnd, setReadToEnd] = useState(false);
  const termsBodyRef = useRef<HTMLDivElement>(null);

  // The user must scroll to the bottom of the terms (including when the content fits without scrolling) before clicking "Agree".
  function handleTermsScroll() {
    const el = termsBodyRef.current;
    if (!el) return;
    if (el.scrollTop + el.clientHeight >= el.scrollHeight - 8) setReadToEnd(true);
  }

  useEffect(() => {
    if (!termsOpen) return;
    // Reset when opened, and handle the case where the content fits on one screen without scrolling.
    setReadToEnd(false);
    const el = termsBodyRef.current;
    if (el && el.scrollHeight <= el.clientHeight + 8) setReadToEnd(true);
  }, [termsOpen]);

  useEffect(() => {
    // Redirect straight to the main view if already logged in (no middleware handles this redirect in static export).
    const token = auth.getToken();
    if (token) {
      // localStorage may still hold credentials while the cookie was lost. Sync first, then trigger a fresh navigation
      // so server guards or route caches don't bounce the redirect back to the login page while still checking.
      auth.setToken(token);
      window.location.replace("/function/tasks");
      return;
    }
    api
      .authStatus()
      .then(({ initialized }) => {
        if (!initialized) router.replace("/setup");
      })
      .catch(() => setError("Unable to connect to backend service"))
      .finally(() => setChecking(false));
  }, [router]);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!agreed) {
      setError("Please read and agree to the Terms of Use first");
      return;
    }
    setLoading(true);
    setError("");
    try {
      const { token } = await api.login("VMT-23", password);
      auth.setToken(token);
      window.location.replace("/function/tasks");
    } catch {
      setError("Invalid username or password");
    } finally {
      setLoading(false);
    }
  }

  if (checking) {
    return (
      <div role="status" className="flex min-h-dvh items-center justify-center text-muted-foreground">
        Checking login status…
      </div>
    );
  }

  return (
    <div className="flex h-dvh">
      {/* Left panel */}
      <div className="hidden flex-col items-center justify-center bg-primary p-12 text-center lg:flex lg:w-1/3">
        <div className="relative flex items-center justify-center">
          <div className="absolute size-80 rounded-full border border-primary-foreground/10" />
          <div className="absolute size-60 rounded-full border border-primary-foreground/15" />
          <div className="absolute size-40 rounded-full border border-primary-foreground/20" />
          {/* eslint-disable-next-line @next/next/no-img-element */}
          <img src="/logo.png" alt="VMT-23" width={160} height={160} className="relative brightness-0 invert" />
        </div>
      </div>

      {/* Right panel */}
      <div className="flex w-full items-center justify-center bg-background p-8 lg:w-2/3">
        <div className="w-full max-w-md space-y-10 py-24 lg:py-32">
          <div className="space-y-4 text-center">
            <h2 className="text-2xl font-medium tracking-tight">Sign In</h2>
            <p className="mx-auto max-w-xl text-muted-foreground">Welcome back. Enter your password to continue to VMT-23.</p>
          </div>
          <form onSubmit={handleSubmit} className="flex flex-col gap-4">
            <div className="space-y-1.5">
              <Label htmlFor="username">Username</Label>
              <Input id="username" value="VMT-23" readOnly className="bg-muted text-muted-foreground" />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="password">Password</Label>
              <Input
                id="password"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder="Enter your password"
                autoFocus
                autoComplete="current-password"
              />
            </div>
            <div className="flex items-start gap-2">
              <Checkbox
                id="agree-terms"
                checked={agreed}
                onCheckedChange={(v) => setAgreed(v === true)}
                className="mt-0.5"
              />
              <Label htmlFor="agree-terms" className="text-sm font-normal leading-relaxed text-muted-foreground">
                I have read and agree to the
                <button
                  type="button"
                  onClick={() => setTermsOpen(true)}
                  className="mx-1 font-medium text-primary underline-offset-4 hover:underline"
                >
                  Terms of Use
                </button>
              </Label>
            </div>
            {error && <p className="text-sm text-destructive">{error}</p>}
            <Button type="submit" className="w-full" disabled={loading || !password || !agreed}>
              {loading ? "Signing in..." : "Sign In"}
            </Button>
          </form>
        </div>
      </div>

      <Dialog open={termsOpen} onOpenChange={setTermsOpen}>
        <DialogContent className="gap-0 p-0 sm:max-w-2xl">
          <DialogHeader className="flex-row items-center gap-3 border-b px-6 py-4">
            <div className="flex size-10 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
              <ShieldCheck className="size-5" />
            </div>
            <div className="space-y-0.5">
              <DialogTitle className="text-base">VMT-23 Terms of Use &amp; Disclaimer</DialogTitle>
              <p className="text-xs text-muted-foreground">
                Version v1.0 · Effective Date: 2026-09-18 · Please read all terms carefully before signing in
              </p>
            </div>
          </DialogHeader>

          <div
            ref={termsBodyRef}
            onScroll={handleTermsScroll}
            className="max-h-[60vh] space-y-5 overflow-y-auto px-6 py-5 text-sm leading-relaxed text-muted-foreground"
          >
            <p className="rounded-lg border bg-muted/40 p-3 text-foreground/80">
              This Terms of Use &amp; Disclaimer (hereinafter referred to as &ldquo;this Statement&rdquo;) constitutes an agreement between you and the VMT-23 project authors and contributors regarding your use of this software. Please read and understand all terms carefully before use, especially the disclaimers, limitations of liability, and prohibitive clauses highlighted in bold or colored blocks.
              <span className="font-medium text-foreground">
                {" "}
                By downloading, installing, accessing, or using this software in any way, you acknowledge that you have read, understood, and agreed to be bound by all terms of this Statement.
              </span>
            </p>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  1
                </span>
                Article 1 · Definitions &amp; Open-Source License
              </h4>
              <p className="pl-7">
                This software (VMT-23) is an open-source program released under the GNU Affero General Public License v3.0 (AGPL-3.0). You may freely use, copy, modify, and distribute this software under the terms of that license; however, any derivative works (including online services provided to third parties over a network) must also be licensed under AGPL-3.0 with complete corresponding source code made available to users. The full terms of AGPL-3.0 are governed by the accompanying LICENSE file.
              </p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  2
                </span>
                Article 2 · Permitted Scope of Use
              </h4>
              <p className="pl-7">
                This software is intended solely for personal learning, code research, exploration of security technology principles, and technical verification within locally isolated environments that you build and control yourself. It is suitable only for non-offensive, non-destructive purposes such as education, academic research, and code review. Except as expressly permitted in this article, you may not use this software for any other purpose.
              </p>
            </section>

            <section className="space-y-2">
              <h4 className="flex items-center gap-2 font-medium text-destructive">
                <span className="flex size-5 items-center justify-center rounded-md bg-destructive/10 text-xs font-semibold text-destructive">
                  3
                </span>
                <AlertTriangle className="size-4" />
                Article 3 · Prohibited Conduct
              </h4>
              <ul className="ml-7 list-decimal space-y-1.5 rounded-lg border border-destructive/20 bg-destructive/5 p-3 pl-8 text-foreground/80 marker:text-destructive/70">
                <li>
                  Strictly prohibited from scanning, probing, exploiting, or attacking any website, online service, or networked system owned by others or third parties (regardless of whether authorization has been obtained or whether the asset belongs to you);
                </li>
                <li>Strictly prohibited from using this software in any real-world penetration testing, offensive/defensive engagements, red/blue team exercises, or production environments;</li>
                <li>Strictly prohibited from using this software for unauthorized intrusion, data theft, extortion, denial-of-service (DoS/DDoS), or any destructive or criminal activities;</li>
                <li>Strictly prohibited from removing, altering, or circumventing any copyright, license, or security notices in this software or its outputs;</li>
                <li>Strictly prohibited from engaging in any activity that violates the laws, regulations, or regulatory requirements of your country or region.</li>
              </ul>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  4
                </span>
                Article 4 · Intellectual Property
              </h4>
              <p className="pl-7">
                The copyright and related intellectual property rights of this software belong to the project authors and contributors, and corresponding rights are granted to you within the scope of the AGPL-3.0 license. Except for the rights expressly granted under that license, this Statement does not grant you any other rights, either express or implied.
              </p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  5
                </span>
                Article 5 · Data &amp; Privacy
              </h4>
              <p className="pl-7">
                This software is a self-hosted open-source program. The authors do not operate any centralized service and do not collect or upload your usage data. All data generated, processed, or accessed during your use of the software is under your sole control, and you are solely responsible for its legality and security; any consequences arising from improper data handling shall be borne by you alone.
              </p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  6
                </span>
                Article 6 · Compliance &amp; Legal Liability
              </h4>
              <p className="pl-7">
                You are solely responsible for complying with all applicable laws and regulations in your country or region regarding cybersecurity, data security, personal information protection, and computer crimes (including, in Mainland China, the Cybersecurity Law, Data Security Law, Personal Information Protection Law, and related judicial interpretations).
                <span className="font-medium text-foreground">
                  {" "}
                  All legal liabilities and consequences arising from your violation of such laws, regulations, or the terms of this Statement shall be borne independently by you, and the authors and contributors of this software shall bear no responsibility.
                </span>
              </p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  7
                </span>
                Article 7 · Disclaimer of Warranties &amp; Limitation of Liability
              </h4>
              <p className="pl-7">
                This software is provided &ldquo;AS IS&rdquo; and &ldquo;AS AVAILABLE&rdquo; without warranties of any kind, either express or implied, including but not limited to warranties of merchantability, fitness for a particular purpose, accuracy, and non-infringement. To the maximum extent permitted by applicable law, the authors and contributors of this software shall not be liable for any direct, indirect, incidental, special, or consequential damages arising out of the use or inability to use this software (whether used properly or improperly), including but not limited to data loss, system damage, business interruption, lost profits, or legal disputes.
              </p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  8
                </span>
                Article 8 · Amendments &amp; Final Interpretation
              </h4>
              <p className="pl-7">
                The authors reserve the right to update this Statement from time to time in accordance with laws and regulations or project needs. Updated versions will be distributed with the project and take effect upon publication; your continued use of this software constitutes acceptance of the revised terms. To the extent permitted by law, the right of final interpretation of this Statement belongs to the project authors. If any provision of this Statement is held to be invalid, the validity of the remaining provisions shall not be affected.
              </p>
            </section>
          </div>

          <DialogFooter className="mx-0 mb-0 flex-col items-stretch gap-2 rounded-b-xl px-6 sm:flex-row sm:items-center sm:justify-between">
            <p className="text-xs text-muted-foreground">
              {readToEnd ? "You have scrolled through all terms" : "Please scroll to the bottom of the terms before confirming"}
            </p>
            <DialogClose asChild>
              <Button
                type="button"
                disabled={!readToEnd}
                onClick={() => {
                  setAgreed(true);
                  setError("");
                }}
              >
                I have read and agree to all terms
              </Button>
            </DialogClose>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
