import type { SVGProps } from 'react';

// S3-style bucket (elliptical rim, tapered body); lucide has no plain bucket.
export function BucketIcon({ className, ...props }: SVGProps<SVGSVGElement>) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round" strokeLinejoin="round"
      className={className} aria-hidden="true" {...props}>
      <ellipse cx="12" cy="6" rx="8" ry="3" />
      <path d="M4 6l2 13c.2 1.4 2.9 2.5 6 2.5s5.8-1.1 6-2.5L20 6" />
    </svg>
  );
}
