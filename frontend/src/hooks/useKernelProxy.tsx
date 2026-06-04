import React, { createContext, useContext } from 'react';

interface KernelProxyContextType {
  kernelProxyUrl: string | undefined;
}

const KernelProxyContext = createContext<KernelProxyContextType>({ kernelProxyUrl: undefined });

export const KernelProxyProvider: React.FC<{ url: string; children: React.ReactNode }> = ({ url, children }) => (
  <KernelProxyContext.Provider value={{ kernelProxyUrl: url }}>
    {children}
  </KernelProxyContext.Provider>
);

export function useKernelProxyUrl(): string | undefined {
  return useContext(KernelProxyContext).kernelProxyUrl;
}
