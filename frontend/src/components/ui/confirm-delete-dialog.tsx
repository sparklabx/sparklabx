import React, { useState } from 'react';
import {
  AlertDialog, AlertDialogContent, AlertDialogHeader, AlertDialogTitle,
  AlertDialogDescription, AlertDialogFooter, AlertDialogCancel,
} from './alert-dialog';
import { Button } from './button';
import { Input } from './input';

interface ConfirmDeleteDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description: string;
  confirmText?: string; // text user must type, default "delete"
  onConfirm: () => void;
}

export const ConfirmDeleteDialog: React.FC<ConfirmDeleteDialogProps> = ({
  open, onOpenChange, title, description, confirmText = 'delete', onConfirm,
}) => {
  const [inputValue, setInputValue] = useState('');

  const handleConfirm = () => {
    if (inputValue.toLowerCase() === confirmText.toLowerCase()) {
      onConfirm();
      setInputValue('');
      onOpenChange(false);
    }
  };

  return (
    <AlertDialog open={open} onOpenChange={(o) => { if (!o) setInputValue(''); onOpenChange(o); }}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>{description}</AlertDialogDescription>
        </AlertDialogHeader>
        <div className="space-y-2">
          <p className="text-sm">Type <span className="font-mono font-bold">{confirmText}</span> to confirm:</p>
          <Input
            value={inputValue}
            onChange={e => setInputValue(e.target.value)}
            onKeyDown={e => { if (e.key === 'Enter') handleConfirm(); }}
            placeholder={confirmText}
            autoFocus
          />
        </div>
        <AlertDialogFooter>
          <AlertDialogCancel onClick={() => setInputValue('')}>Cancel</AlertDialogCancel>
          <Button
            variant="destructive"
            onClick={handleConfirm}
            disabled={inputValue.toLowerCase() !== confirmText.toLowerCase()}
          >
            Delete
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
};
