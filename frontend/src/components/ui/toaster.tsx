"use client"

import { useToast } from "@/hooks/use-toast"
import { X } from "lucide-react"

export function Toaster() {
    const { toasts, dismiss } = useToast()

    return (
        <div className="fixed bottom-4 right-4 z-[100] flex flex-col gap-2 max-w-md">
            {toasts.map((toast) => (
                <div
                    key={toast.id}
                    className={`
            flex items-start gap-3 p-4 rounded-lg shadow-lg border backdrop-blur-sm
            animate-in slide-in-from-bottom-5 fade-in duration-300
            ${toast.variant === "destructive"
                            ? "bg-destructive/90 text-destructive-foreground border-destructive"
                            : toast.variant === "success"
                                ? "bg-green-600/90 text-white border-green-600"
                                : "bg-background/95 text-foreground border-border"}
          `}
                >
                    <div className="flex-1 min-w-0">
                        {toast.title && (
                            <p className="font-medium text-sm">{toast.title}</p>
                        )}
                        {toast.description && (
                            <p className="text-xs opacity-90 mt-1">{toast.description}</p>
                        )}
                    </div>
                    <button
                        onClick={() => dismiss(toast.id)}
                        className="shrink-0 opacity-70 hover:opacity-100 transition-opacity"
                    >
                        <X className="h-4 w-4" />
                    </button>
                </div>
            ))}
        </div>
    )
}
