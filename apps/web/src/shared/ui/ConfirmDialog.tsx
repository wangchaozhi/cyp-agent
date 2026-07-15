import { useEffect, useId, useRef } from "react";
import type { KeyboardEvent as ReactKeyboardEvent, ReactNode } from "react";
import { createPortal } from "react-dom";

import "./ConfirmDialog.css";

const FOCUSABLE_SELECTOR = [
  "a[href]",
  "area[href]",
  "button:not([disabled])",
  "input:not([disabled]):not([type='hidden'])",
  "select:not([disabled])",
  "textarea:not([disabled])",
  "[contenteditable='true']",
  "[tabindex]:not([tabindex='-1'])",
].join(",");

let bodyScrollLockCount = 0;
let previousBodyOverflow = "";
let previousBodyPaddingRight = "";

export type ConfirmDialogTone = "default" | "danger";
export type ConfirmDialogInitialFocus = "cancel" | "confirm";

export interface ConfirmDialogProps {
  /** Controls whether the dialog is mounted. */
  open: boolean;
  /** A short, action-specific heading. */
  title: ReactNode;
  /** Explains the consequence of confirming the action. */
  description: ReactNode;
  /** Optional contextual data, such as symbol, quantity, or current price. */
  details?: ReactNode;
  detailsLabel?: ReactNode;
  cancelLabel?: ReactNode;
  confirmLabel?: ReactNode;
  busyLabel?: ReactNode;
  tone?: ConfirmDialogTone;
  busy?: boolean;
  initialFocus?: ConfirmDialogInitialFocus;
  dismissOnBackdrop?: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}

function getFocusableElements(container: HTMLElement): HTMLElement[] {
  return Array.from(container.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR)).filter((element) => {
    if (element.hidden || element.getAttribute("aria-hidden") === "true") return false;
    const style = window.getComputedStyle(element);
    return style.display !== "none" && style.visibility !== "hidden";
  });
}

function lockBodyScroll(): () => void {
  if (bodyScrollLockCount === 0) {
    previousBodyOverflow = document.body.style.overflow;
    previousBodyPaddingRight = document.body.style.paddingRight;

    const scrollbarWidth = window.innerWidth - document.documentElement.clientWidth;
    if (scrollbarWidth > 0) {
      const currentPadding = Number.parseFloat(window.getComputedStyle(document.body).paddingRight) || 0;
      document.body.style.paddingRight = `${currentPadding + scrollbarWidth}px`;
    }
    document.body.style.overflow = "hidden";
  }

  bodyScrollLockCount += 1;
  return () => {
    bodyScrollLockCount = Math.max(0, bodyScrollLockCount - 1);
    if (bodyScrollLockCount === 0) {
      document.body.style.overflow = previousBodyOverflow;
      document.body.style.paddingRight = previousBodyPaddingRight;
    }
  };
}

export function ConfirmDialog({
  open,
  title,
  description,
  details,
  detailsLabel = "操作详情",
  cancelLabel = "取消",
  confirmLabel = "确认",
  busyLabel = "处理中…",
  tone = "danger",
  busy = false,
  initialFocus = "cancel",
  dismissOnBackdrop = false,
  onCancel,
  onConfirm,
}: ConfirmDialogProps) {
  const titleId = useId();
  const descriptionId = useId();
  const detailsId = useId();
  const dialogRef = useRef<HTMLDivElement | null>(null);
  const cancelButtonRef = useRef<HTMLButtonElement | null>(null);
  const confirmButtonRef = useRef<HTMLButtonElement | null>(null);
  const previousFocusRef = useRef<HTMLElement | null>(null);
  const onCancelRef = useRef(onCancel);
  const initialFocusRef = useRef(initialFocus);

  onCancelRef.current = onCancel;
  initialFocusRef.current = initialFocus;

  useEffect(() => {
    if (!open) return undefined;

    previousFocusRef.current = document.activeElement instanceof HTMLElement
      ? document.activeElement
      : null;
    const unlockBodyScroll = lockBodyScroll();

    const focusInsideDialog = () => {
      const preferredButton = initialFocusRef.current === "confirm"
        ? confirmButtonRef.current
        : cancelButtonRef.current;
      const fallbackButton = initialFocusRef.current === "confirm"
        ? cancelButtonRef.current
        : confirmButtonRef.current;
      const target = preferredButton && !preferredButton.disabled
        ? preferredButton
        : fallbackButton && !fallbackButton.disabled
          ? fallbackButton
          : dialogRef.current;
      target?.focus({ preventScroll: true });
    };

    const animationFrame = window.requestAnimationFrame(focusInsideDialog);

    const handleKeyDown = (event: globalThis.KeyboardEvent) => {
      const dialog = dialogRef.current;
      if (!dialog) return;

      if (event.key === "Escape") {
        event.preventDefault();
        event.stopPropagation();
        onCancelRef.current();
        return;
      }

      if (event.key !== "Tab") return;
      const focusableElements = getFocusableElements(dialog);
      if (focusableElements.length === 0) {
        event.preventDefault();
        dialog.focus({ preventScroll: true });
        return;
      }

      const first = focusableElements[0];
      const last = focusableElements[focusableElements.length - 1];
      const activeElement = document.activeElement;
      const focusIsOutside = !(activeElement instanceof Node) || !dialog.contains(activeElement);

      if (event.shiftKey && (focusIsOutside || activeElement === first)) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && (focusIsOutside || activeElement === last)) {
        event.preventDefault();
        first.focus();
      }
    };

    const handleFocusIn = (event: FocusEvent) => {
      const dialog = dialogRef.current;
      if (!dialog || !(event.target instanceof Node) || dialog.contains(event.target)) return;
      focusInsideDialog();
    };

    document.addEventListener("keydown", handleKeyDown, true);
    document.addEventListener("focusin", handleFocusIn, true);

    return () => {
      window.cancelAnimationFrame(animationFrame);
      document.removeEventListener("keydown", handleKeyDown, true);
      document.removeEventListener("focusin", handleFocusIn, true);
      unlockBodyScroll();

      const previousFocus = previousFocusRef.current;
      if (previousFocus?.isConnected) previousFocus.focus({ preventScroll: true });
    };
  }, [open]);

  if (!open || typeof document === "undefined") return null;

  const describedBy = details === undefined
    ? descriptionId
    : `${descriptionId} ${detailsId}`;

  const handleDialogKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    // Keep Enter on the dialog surface from accidentally confirming. Buttons
    // retain their normal keyboard behavior.
    if (event.key === "Enter" && event.target === event.currentTarget) {
      event.preventDefault();
    }
  };

  return createPortal(
    <div
      className="confirm-dialog-backdrop"
      role="presentation"
      onMouseDown={(event) => {
        if (dismissOnBackdrop && event.target === event.currentTarget) onCancel();
      }}
    >
      <div
        ref={dialogRef}
        className="confirm-dialog"
        data-tone={tone}
        role={tone === "danger" ? "alertdialog" : "dialog"}
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={describedBy}
        aria-busy={busy || undefined}
        tabIndex={-1}
        onKeyDown={handleDialogKeyDown}
      >
        <div className="confirm-dialog__content">
          <div className="confirm-dialog__heading">
            <span className="confirm-dialog__icon" aria-hidden="true">
              {tone === "danger" ? "!" : "?"}
            </span>
            <div>
              <h2 id={titleId}>{title}</h2>
              <div id={descriptionId} className="confirm-dialog__description">
                {description}
              </div>
            </div>
          </div>

          {details !== undefined ? (
            <div id={detailsId} className="confirm-dialog__details">
              <div className="confirm-dialog__details-label">{detailsLabel}</div>
              <div className="confirm-dialog__details-content">{details}</div>
            </div>
          ) : null}
        </div>

        <div className="confirm-dialog__actions">
          <button
            ref={cancelButtonRef}
            className="confirm-dialog__button confirm-dialog__button--cancel"
            type="button"
            disabled={busy}
            onClick={onCancel}
          >
            {cancelLabel}
          </button>
          <button
            ref={confirmButtonRef}
            className="confirm-dialog__button confirm-dialog__button--confirm"
            type="button"
            disabled={busy}
            onClick={onConfirm}
          >
            {busy ? <span className="confirm-dialog__spinner" aria-hidden="true" /> : null}
            {busy ? busyLabel : confirmLabel}
          </button>
        </div>
      </div>
    </div>,
    document.body,
  );
}
