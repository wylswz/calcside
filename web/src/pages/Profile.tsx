import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { api, User } from '../api'
import { Badge, Button, Field, Notice, PageHeader, SectionTitle, inputCls } from '../components/ui'

export default function Profile({ user }: { user: User }) {
  const [currentPassword, setCurrentPassword] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [confirmation, setConfirmation] = useState('')
  const [validationError, setValidationError] = useState('')
  const changePassword = useMutation({
    mutationFn: () => api.post('/api/v1/me/password', { current_password: currentPassword, new_password: newPassword }),
    onSuccess: () => {
      setCurrentPassword('')
      setNewPassword('')
      setConfirmation('')
      window.location.replace('/login?password_changed=1')
    },
  })
  const error = validationError || changePassword.error?.message

  return (
    <div className="max-w-xl">
      <PageHeader index="06" section="Account" title="Profile">Your account and sign-in settings.</PageHeader>
      <SectionTitle>Account</SectionTitle>
      <dl className="mb-10 grid grid-cols-[auto_1fr] gap-x-8 gap-y-3 text-sm">
        <dt className="text-sec">Name</dt><dd className="text-ink">{user.name || '—'}</dd>
        <dt className="text-sec">{user.username ? 'Login name' : 'Email'}</dt><dd className="break-all text-ink">{user.username || user.email}</dd>
        <dt className="text-sec">Role</dt><dd><Badge tone={user.is_admin ? 'blue' : 'gray'}>{user.is_admin ? 'Administrator' : 'User'}</Badge></dd>
      </dl>
      <SectionTitle>Password</SectionTitle>
      {user.has_password ? (
        <form className="space-y-4" onSubmit={(e) => {
          e.preventDefault()
          changePassword.reset()
          setValidationError('')
          const bytes = new TextEncoder().encode(newPassword).length
          if (bytes < 8 || bytes > 72) {
            setValidationError('Password must contain 8–72 UTF-8 bytes.')
          } else if (newPassword !== confirmation) {
            setValidationError('New passwords do not match.')
          } else {
            changePassword.mutate()
          }
        }}>
          <p className="text-sm text-sec">Changing your password signs out all current sessions. API keys are not affected.</p>
          <Field label="Current password">
            <input className={inputCls} type="password" name="current-password" autoComplete="current-password" required
              value={currentPassword} onChange={(e) => setCurrentPassword(e.target.value)} disabled={changePassword.isPending} />
          </Field>
          <Field label="New password">
            <input className={inputCls} type="password" name="new-password" autoComplete="new-password" required maxLength={72}
              value={newPassword} onChange={(e) => setNewPassword(e.target.value)} disabled={changePassword.isPending} />
          </Field>
          <Field label="Confirm new password">
            <input className={inputCls} type="password" name="confirm-password" autoComplete="new-password" required maxLength={72}
              value={confirmation} onChange={(e) => setConfirmation(e.target.value)} disabled={changePassword.isPending} />
          </Field>
          {error && <div role="alert"><Notice tone="red">{error}</Notice></div>}
          <Button type="submit" variant="primary" disabled={changePassword.isPending}>
            {changePassword.isPending ? 'Changing password…' : 'Change password'}
          </Button>
        </form>
      ) : (
        <Notice>This account does not use a local password. Manage your sign-in credentials with your identity provider.</Notice>
      )}
    </div>
  )
}
