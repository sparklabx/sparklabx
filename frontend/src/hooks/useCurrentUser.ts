import { useEffect, useState } from 'react';
import authService from '@/services/authService';

// useCurrentUser returns the currently-logged-in user and re-renders when
// auth state changes (login, logout, role refresh, switch account). Use this
// instead of calling authService.getCurrentUser() / isSuperAdmin() directly
// inside components — those are static reads and won't update without reload.
export function useCurrentUser() {
    const [user, setUser] = useState(() => authService.getCurrentUser());
    useEffect(() => {
        return authService.subscribe(() => setUser(authService.getCurrentUser()));
    }, []);
    return {
        user,
        isSuperAdmin: (user as any)?.admin_role === 'superadmin',
        isAdmin: (user as any)?.role === 'admin',
    };
}
