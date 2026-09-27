package api

import "errors"

func autoMaintenanceAuthorityID(a autoMaintenanceAuthority) string {
	base := autoSHA([]byte(a.ResourceID + ":" + a.RegistrySHA + ":" + a.BeforeSHA + ":" + a.CandidateSHA))
	if a.PredecessorOperationID != "" {
		return autoSHA([]byte(base + ":successor:" + a.PredecessorOperationID + ":" + a.SupersessionSHA))
	}
	return base
}
func autoValidateMaintenanceSuccessor(l *autoMaintenanceLedger, a autoMaintenanceAuthority) error {
	if l == nil || !autoHash256(a.PredecessorOperationID) || !autoHash256(a.SupersessionSHA) {
		return errors.New("maintenance predecessor evidence missing")
	}
	old := l.Operations[a.PredecessorOperationID]
	if old == nil || old.State != "superseded" || old.Authority.ResourceID != a.ResourceID || old.ExternalResolution == nil || old.ExternalResolution.OperationID != old.ID || old.ExternalResolution.ReceiptSHA != a.SupersessionSHA || old.ExternalResolution.RegistrySHA != old.Authority.RegistrySHA || !old.ExternalResolution.Healthy || !old.ExternalResolution.NoMutation || !old.ExternalResolution.OwnershipChecked || old.ExternalResolution.OwnedCandidate || old.ExternalResolution.ConflictReceiptSHA != old.ConflictReceiptSHA || !autoHash256(old.ConflictReceiptSHA) {
		return errors.New("maintenance predecessor is not an authenticated healthy external generation")
	}
	if l.Owners[a.ResourceID] != old.ID {
		return errors.New("maintenance predecessor superseded by a later resource owner")
	}
	return nil
}

// Called before plan audits. A new title, observation UUID, task, or generation
// never creates a successor. Only actual resolved external ownership does.
func autoPinMaintenanceSuccessor(l *autoMaintenanceLedger, pin *autoMaintenancePlanPin) error {
	if l == nil {
		return nil
	}
	authority := autoMaintenancePinAuthority(*pin)
	base := autoMaintenanceAuthorityID(authority)
	if l.Operations[base] == nil {
		return nil
	}
	prior := l.Operations[l.Owners[authority.ResourceID]]
	if prior == nil {
		return errors.New("prior maintenance operation has unresolved ownership")
	}
	authority.PredecessorOperationID = prior.ID
	if prior.ExternalResolution != nil {
		authority.SupersessionSHA = prior.ExternalResolution.ReceiptSHA
	}
	if err := autoValidateMaintenanceSuccessor(l, authority); err != nil {
		return err
	}
	successor := autoMaintenanceAuthorityID(authority)
	if l.Operations[successor] != nil {
		return errors.New("this external generation already admitted its maintenance successor")
	}
	pin.PredecessorOperationID = authority.PredecessorOperationID
	pin.SupersessionSHA = authority.SupersessionSHA
	pin.Key = autoMaintenancePinHash(*pin)
	return nil
}
