import { http } from './client'
import type { Dashboard } from '@/types/dashboard'

export const get = () => http.get<Dashboard>('/dashboard')
