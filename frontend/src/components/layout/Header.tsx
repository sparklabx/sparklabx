import { useState } from 'react';
import { cn } from '@/lib/utils';
import { ThemeToggle } from '@/components/theme-toggle';

interface HeaderProps {
  className?: string;
}

export function Header({ className }: HeaderProps) {
  return (
    <header
      className={cn(
        "sticky top-0 z-40 w-full h-16 border-b border-border bg-background/95 backdrop-blur supports-[backdrop-filter]:bg-background/60",
        className
      )}
    >
      <div className="flex h-full items-center justify-between px-6">
        {/* Left side - Title */}
        <div className="flex items-center gap-4">
          <h1 className="text-heading-sm text-foreground">
            SparkLabX
          </h1>
          <div className="hidden md:flex items-center gap-2">
            <div className="h-4 w-px bg-border" />
            <span className="text-sm text-muted-foreground">Production</span>
          </div>
        </div>

        {/* Right side - Actions */}
        <div className="flex items-center gap-3">
          {/* Theme Toggle */}
          <ThemeToggle />
        </div>
      </div>
    </header>
  );
}
