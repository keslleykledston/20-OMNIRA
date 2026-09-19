import React, { useState } from 'react';
import axios from 'axios';
import { API_BASE } from '../lib/config';
import { getAuthToken, getTenantId } from '../lib/session';


interface AssignmentButtonProps {
  conversationId: string;
  assignedToUserId?: string;
  onAssignmentChange?: (userId: string) => void;
}

/**
 * M05.4 — AssignmentButton
 * Consumes POST /tenants/{tid}/inbox/conversations/{cid}/assign|unassign (tenant and actor come from the JWT, never the body)
 * Supports manual claim and unassignment
 */
export function AssignmentButton({ conversationId, assignedToUserId, onAssignmentChange }: AssignmentButtonProps) {
  const tenantId = getTenantId();
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const handleAssign = async () => {
    setIsLoading(true);
    setError(null);

    try {
      const response = await axios.post(
        `${API_BASE}/tenants/${tenantId}/inbox/conversations/${conversationId}/assign`,
        {},
        {
          headers: {
            Authorization: `Bearer ${getJWTToken()}`,
          },
        }
      );

      if (response.status === 200) {
        const data = response.data;
        onAssignmentChange?.(data.assigned_to_user_id ?? '');
      }
    } catch (err: any) {
      setError(assignmentErrorMessage(err, 'Failed to assign conversation'));
    } finally {
      setIsLoading(false);
    }
  };

  const handleUnassign = async () => {
    setIsLoading(true);
    setError(null);

    try {
      await axios.post(
        `${API_BASE}/tenants/${tenantId}/inbox/conversations/${conversationId}/unassign`,
        {},
        {
          headers: {
            Authorization: `Bearer ${getJWTToken()}`,
          },
        }
      );

      onAssignmentChange?.('');
    } catch (err: any) {
      setError(assignmentErrorMessage(err, 'Failed to unassign conversation'));
    } finally {
      setIsLoading(false);
    }
  };

  return (
    <div className="assignment-button-container">
      {assignedToUserId ? (
        <div className="assigned-state">
          <span className="assigned-badge">Assigned to {assignedToUserId.substring(0, 8)}</span>
          <button
            className="unassign-btn"
            onClick={handleUnassign}
            disabled={isLoading}
          >
            {isLoading ? 'Unassigning...' : 'Release'}
          </button>
        </div>
      ) : (
        <button
          className="claim-btn"
          onClick={handleAssign}
          disabled={isLoading}
        >
          {isLoading ? 'Assigning...' : 'Assign to Me'}
        </button>
      )}

      {error && <div className="error-message">{error}</div>}

      <style>{`
        .assignment-button-container {
          display: flex;
          gap: 8px;
          align-items: center;
          padding: 12px;
          background: #f9f9f9;
          border-radius: 8px;
          border: 1px solid #e0e0e0;
        }

        .assigned-state {
          display: flex;
          gap: 8px;
          align-items: center;
          width: 100%;
        }

        .assigned-badge {
          padding: 6px 12px;
          background: #007AFF;
          color: white;
          border-radius: 6px;
          font-size: 13px;
          font-weight: 500;
          flex-grow: 1;
        }

        .claim-btn,
        .unassign-btn {
          padding: 8px 16px;
          border: none;
          border-radius: 6px;
          font-weight: 600;
          cursor: pointer;
          transition: all 0.2s;
          font-size: 13px;
        }

        .claim-btn {
          background: #34C759;
          color: white;
        }

        .claim-btn:hover:not(:disabled) {
          background: #2ba84c;
        }

        .claim-btn:disabled {
          opacity: 0.6;
          cursor: not-allowed;
        }

        .unassign-btn {
          background: #ff3b30;
          color: white;
          padding: 8px 12px;
        }

        .unassign-btn:hover:not(:disabled) {
          background: #e63228;
        }

        .unassign-btn:disabled {
          opacity: 0.6;
          cursor: not-allowed;
        }

        .error-message {
          color: #ff3b30;
          font-size: 12px;
          margin-top: 8px;
          padding: 4px 8px;
          background: #fff5f5;
          border-radius: 4px;
        }
      `}</style>
    </div>
  );
}

// Backend answers plain-text errors: 409 lost the claim race, 403 lacks the
// permission, 404 conversation not visible in this tenant.
export function assignmentErrorMessage(err: any, fallback: string): string {
  switch (err?.response?.status) {
    case 409:
      return 'Already assigned to another agent';
    case 403:
      return 'You do not have permission to do this';
    case 404:
      return 'Conversation not found';
    default:
      return fallback;
  }
}

function getJWTToken(): string {
  return getAuthToken();
}
