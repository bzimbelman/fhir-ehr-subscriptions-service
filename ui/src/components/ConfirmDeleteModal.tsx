"use client";

import { useEffect, useRef } from "react";

/**
 * Confirmation modal for bulk-delete from the DLQ.
 *
 * Why a modal: delete is destructive and irreversible (the backend purges
 * the row, no undo). The Mirth reference doc explicitly calls out that we
 * want unambiguous Replay vs Discard semantics -- a confirmation step on
 * delete makes "I clicked the wrong button" survivable.
 *
 * Accessibility:
 *   - Focus moves to the Cancel button on open (Cancel is the safe default).
 *   - Escape closes the modal (-> onCancel).
 *   - Backdrop click closes (-> onCancel).
 *   - role="dialog" + aria-modal + aria-labelledby so screen readers
 *     announce it correctly.
 */
interface ConfirmDeleteModalProps {
  open: boolean;
  count: number;
  onConfirm: () => void;
  onCancel: () => void;
}

export function ConfirmDeleteModal({
  open,
  count,
  onConfirm,
  onCancel,
}: ConfirmDeleteModalProps) {
  const cancelRef = useRef<HTMLButtonElement | null>(null);

  useEffect(() => {
    if (!open) return;
    cancelRef.current?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onCancel();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [open, onCancel]);

  if (!open) return null;

  return (
    <div
      data-testid="confirm-delete-backdrop"
      onClick={onCancel}
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4"
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="confirm-delete-title"
        data-testid="confirm-delete-modal"
        onClick={(e) => e.stopPropagation()}
        className="w-full max-w-md rounded-lg bg-white p-5 shadow-lg"
      >
        <h2
          id="confirm-delete-title"
          className="mb-2 text-base font-semibold text-gray-900"
        >
          Discard {count} message{count === 1 ? "" : "s"} from the DLQ?
        </h2>
        <p className="mb-4 text-sm text-gray-700">
          This permanently removes the selected dead-letter row
          {count === 1 ? "" : "s"}. The action is recorded in the audit log
          but cannot be undone.
        </p>
        <div className="flex justify-end gap-2">
          <button
            ref={cancelRef}
            type="button"
            onClick={onCancel}
            data-testid="confirm-delete-cancel"
            className="rounded border border-gray-300 bg-white px-3 py-1.5 text-sm text-gray-800 hover:bg-gray-100"
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={onConfirm}
            data-testid="confirm-delete-confirm"
            className="rounded bg-red-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-red-700 focus:outline-none focus-visible:ring-2 focus-visible:ring-red-500"
          >
            Discard {count}
          </button>
        </div>
      </div>
    </div>
  );
}
