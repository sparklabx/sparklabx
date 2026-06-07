import React, { useState } from 'react';
import { GoogleOAuthProvider, useGoogleLogin } from '@react-oauth/google';
import { PublicClientApplication } from '@azure/msal-browser';
import { Card, CardContent, CardHeader } from '../ui/card';
import { Input } from '../ui/input';
import { Label } from '../ui/label';
import { Button } from '../ui/button';
import authService from '../../services/authService';

const GOOGLE_CLIENT_ID = import.meta.env.VITE_GOOGLE_CLIENT_ID || '';
const MICROSOFT_CLIENT_ID = import.meta.env.VITE_MICROSOFT_CLIENT_ID || '';
const MICROSOFT_TENANT_ID = import.meta.env.VITE_MICROSOFT_TENANT_ID || '';

// MSAL throws ClientConfigurationError when constructed with an empty
// clientId. Skip instantiation entirely when Microsoft login isn't
// configured (README documents OAuth as optional).
const msalInstance = MICROSOFT_CLIENT_ID
  ? new PublicClientApplication({
      auth: {
        clientId: MICROSOFT_CLIENT_ID,
        authority: `https://login.microsoftonline.com/${MICROSOFT_TENANT_ID}`,
        redirectUri: window.location.origin,
      },
      cache: { cacheLocation: 'sessionStorage' as const },
    })
  : null;

const msalReady: Promise<boolean> = msalInstance
  ? (() => {
      const inst = msalInstance;
      return inst.initialize()
        .then(() => inst.handleRedirectPromise())
        .then(() => true)
        .catch(() => true);
    })()
  : Promise.resolve(false);

const btnClass = "flex items-center justify-center w-full h-[40px] border border-[#dadce0] rounded bg-white hover:bg-[#f7f8f8] disabled:opacity-50 transition-colors gap-3";
const btnTextClass = "text-[14px] text-[#3c4043] font-medium";

interface LoginProps {
  onSuccess: () => void;
}

// Google icon SVG
const GoogleIcon = () => (
  <svg width="18" height="18" viewBox="0 0 48 48">
    <path fill="#EA4335" d="M24 9.5c3.54 0 6.71 1.22 9.21 3.6l6.85-6.85C35.9 2.38 30.47 0 24 0 14.62 0 6.51 5.38 2.56 13.22l7.98 6.19C12.43 13.72 17.74 9.5 24 9.5z"/>
    <path fill="#4285F4" d="M46.98 24.55c0-1.57-.15-3.09-.38-4.55H24v9.02h12.94c-.58 2.96-2.26 5.48-4.78 7.18l7.73 6c4.51-4.18 7.09-10.36 7.09-17.65z"/>
    <path fill="#FBBC05" d="M10.53 28.59c-.48-1.45-.76-2.99-.76-4.59s.27-3.14.76-4.59l-7.98-6.19C.92 16.46 0 20.12 0 24c0 3.88.92 7.54 2.56 10.78l7.97-6.19z"/>
    <path fill="#34A853" d="M24 48c6.48 0 11.93-2.13 15.89-5.81l-7.73-6c-2.15 1.45-4.92 2.3-8.16 2.3-6.26 0-11.57-4.22-13.47-9.91l-7.98 6.19C6.51 42.62 14.62 48 24 48z"/>
  </svg>
);

// Microsoft icon SVG
const MicrosoftIcon = () => (
  <svg width="18" height="18" viewBox="0 0 21 21">
    <rect x="1" y="1" width="9" height="9" fill="#f25022"/>
    <rect x="11" y="1" width="9" height="9" fill="#7fba00"/>
    <rect x="1" y="11" width="9" height="9" fill="#00a4ef"/>
    <rect x="11" y="11" width="9" height="9" fill="#ffb900"/>
  </svg>
);

interface GoogleSignInButtonProps {
  disabled: boolean;
  onSignIn: (accessToken: string) => Promise<void>;
  onError: () => void;
}

// Isolated so useGoogleLogin (which requires GoogleOAuthProvider context)
// is only called when GOOGLE_CLIENT_ID is set and the provider is mounted.
const GoogleSignInButton: React.FC<GoogleSignInButtonProps> = ({ disabled, onSignIn, onError }) => {
  const googleLogin = useGoogleLogin({
    onSuccess: (tokenResponse) => onSignIn(tokenResponse.access_token),
    onError,
  });
  return (
    <button onClick={() => googleLogin()} disabled={disabled} className={btnClass}>
      <GoogleIcon />
      <span className={btnTextClass}>Sign in with Google</span>
    </button>
  );
};

const LoginForm: React.FC<LoginProps> = ({ onSuccess }) => {
  const [identifier, setIdentifier] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const [showPassword, setShowPassword] = useState(false);

  const handleGoogleLogin = async (accessToken: string) => {
    setError('');
    setLoading(true);
    try {
      await authService.loginWithGoogle(accessToken);
      onSuccess();
    } catch (err: any) {
      setError(err.response?.data?.error || 'Google login failed');
    } finally {
      setLoading(false);
    }
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
    setLoading(true);
    try {
      await authService.login(identifier, password);
      onSuccess();
    } catch (err: any) {
      setError(err.response?.data?.error || 'Login failed');
    } finally {
      setLoading(false);
    }
  };

  const handleMicrosoftLogin = async () => {
    if (!msalInstance) return;
    setError('');
    setLoading(true);
    try {
      await msalReady;
      const result = await msalInstance.loginPopup({
        scopes: ['User.Read'],
        prompt: 'select_account',
      });
      if (result.accessToken) {
        await authService.loginWithMicrosoft(result.accessToken);
        onSuccess();
      }
    } catch (err: any) {
      if (err.errorCode !== 'user_cancelled') {
        setError(err.message || 'Microsoft login failed');
      }
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="flex items-center justify-center min-h-screen bg-background">
      <Card className="w-full max-w-sm">
        <CardHeader className="text-center space-y-3 pb-2">
          <img src="/logo.png" alt="SparkLabX" className="h-12 mx-auto dark:hidden" />
          <img src="/logo-dark.png?v=2" alt="SparkLabX" className="h-12 mx-auto hidden dark:block" />
        </CardHeader>
        <CardContent className="space-y-3">
          {error && (
            <div className="p-2.5 text-sm text-red-500 bg-red-50 dark:bg-red-950 rounded-md">
              {error}
            </div>
          )}

          {/* OAuth buttons are hidden when their client ID isn't configured —
              this lets a stock self-host work via username/password only. */}
          {GOOGLE_CLIENT_ID && (
            <GoogleSignInButton
              disabled={loading}
              onSignIn={handleGoogleLogin}
              onError={() => setError('Google login failed')}
            />
          )}

          {MICROSOFT_CLIENT_ID && (
            <button onClick={handleMicrosoftLogin} disabled={loading} className={btnClass}>
              <MicrosoftIcon />
              <span className={btnTextClass}>Sign in with Microsoft</span>
            </button>
          )}

          {(GOOGLE_CLIENT_ID || MICROSOFT_CLIENT_ID) && (
            <div className="relative">
              <div className="absolute inset-0 flex items-center">
                <span className="w-full border-t" />
              </div>
              <div className="relative flex justify-center text-xs uppercase">
                <span className="bg-card px-2 text-muted-foreground">or</span>
              </div>
            </div>
          )}

          {/* Manual login */}
          <button
            type="button"
            onClick={() => setShowPassword(!showPassword)}
            className="w-full text-center text-sm text-muted-foreground hover:text-primary transition-colors"
          >
            {showPassword ? 'Hide manual login' : 'Login with username/password'}
          </button>

          {showPassword && (
            <form onSubmit={handleSubmit} className="space-y-3">
              <div className="space-y-1.5">
                <Label htmlFor="identifier" className="text-xs">Username or Email</Label>
                <Input
                  id="identifier"
                  placeholder="admin or admin@sparklabx.com"
                  value={identifier}
                  onChange={(e) => setIdentifier(e.target.value)}
                  className="h-9"
                  autoFocus
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="password" className="text-xs">Password</Label>
                <Input
                  id="password"
                  type="password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  className="h-9"
                />
              </div>
              <Button type="submit" className="w-full h-9" disabled={loading}>
                {loading ? 'Logging in...' : 'Sign in'}
              </Button>
            </form>
          )}
        </CardContent>
      </Card>
    </div>
  );
};

const LoginPage: React.FC<LoginProps> = ({ onSuccess }) => {
  // Only mount GoogleOAuthProvider when a client ID is configured. Mounting
  // it with an empty clientId eagerly loads accounts.google.com/gsi/client
  // and throws "Missing required parameter client_id." at runtime.
  if (!GOOGLE_CLIENT_ID) {
    return <LoginForm onSuccess={onSuccess} />;
  }
  return (
    <GoogleOAuthProvider clientId={GOOGLE_CLIENT_ID}>
      <LoginForm onSuccess={onSuccess} />
    </GoogleOAuthProvider>
  );
};

export default LoginPage;
