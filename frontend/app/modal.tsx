"use client";
import { useEffect, useRef, type ReactNode } from "react";

export default function Modal({ children, titleId, className = "", busy = false, onClose }: {
  children: ReactNode; titleId: string; className?: string; busy?: boolean; onClose: () => void;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const dialog = ref.current;
    const previous = document.activeElement as HTMLElement | null;
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    dialog?.showModal();
    return () => { dialog?.close(); document.body.style.overflow = previousOverflow; previous?.focus(); };
  }, []);
  return <dialog ref={ref} className={`dialog native-dialog ${className}`} aria-labelledby={titleId}
    onCancel={(event) => { event.preventDefault(); if (!busy) onClose(); }}>
    {children}
  </dialog>;
}
