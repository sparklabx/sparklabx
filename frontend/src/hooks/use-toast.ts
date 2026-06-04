import { useEffect, useState } from "react"

type ToastVariant = "default" | "destructive" | "success"

interface Toast {
    id: string
    title?: string
    description?: string
    variant?: ToastVariant
}

interface ToastState {
    toasts: Toast[]
}

let toastCount = 0

function genId() {
    toastCount = (toastCount + 1) % Number.MAX_VALUE
    return toastCount.toString()
}

const listeners: Array<(state: ToastState) => void> = []
let memoryState: ToastState = { toasts: [] }

function dispatch(action: { type: "ADD_TOAST" | "DISMISS_TOAST"; toast?: Toast; toastId?: string }) {
    memoryState = reducer(memoryState, action)
    listeners.forEach((listener) => {
        listener(memoryState)
    })
}

function reducer(state: ToastState, action: { type: string; toast?: Toast; toastId?: string }): ToastState {
    switch (action.type) {
        case "ADD_TOAST":
            return {
                ...state,
                toasts: [action.toast!, ...state.toasts].slice(0, 5),
            }
        case "DISMISS_TOAST":
            return {
                ...state,
                toasts: state.toasts.filter((t) => t.id !== action.toastId),
            }
        default:
            return state
    }
}

function toast({ title, description, variant = "default" }: { title?: string; description?: string; variant?: ToastVariant }) {
    const id = genId()

    dispatch({
        type: "ADD_TOAST",
        toast: { id, title, description, variant },
    })

    // Auto dismiss after 3 seconds
    setTimeout(() => {
        dispatch({ type: "DISMISS_TOAST", toastId: id })
    }, 3000)

    return {
        id,
        dismiss: () => dispatch({ type: "DISMISS_TOAST", toastId: id }),
    }
}

function useToast() {
    const [state, setState] = useState<ToastState>(memoryState)

    useEffect(() => {
        listeners.push(setState)
        return () => {
            const index = listeners.indexOf(setState)
            if (index > -1) {
                listeners.splice(index, 1)
            }
        }
    }, [])

    return {
        ...state,
        toast,
        dismiss: (toastId: string) => dispatch({ type: "DISMISS_TOAST", toastId }),
    }
}

export { useToast, toast }
